package main

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const authCookieName = "vless_manager_session"

var errInvalidCredentials = errors.New("invalid credentials")

type credentialAuthenticator interface {
	Authenticate(context.Context, string, string) error
}

type keeneticAuthenticator struct {
	baseURL string
	client  *http.Client
}

func newKeeneticAuthenticator(baseURL string) *keeneticAuthenticator {
	transport := http.DefaultTransport
	if strings.TrimSpace(baseURL) == "" {
		// Authentication talks to the router itself. Mark the socket so global
		// OUTPUT routing cannot send this request into the managed VPN tunnel.
		transport = &http.Transport{
			DialContext:       wanDialer(6 * time.Second).DialContext,
			DisableKeepAlives: true,
		}
	}
	return &keeneticAuthenticator{
		baseURL: strings.TrimRight(baseURL, "/"),
		client: &http.Client{
			Timeout:   6 * time.Second,
			Transport: transport,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

func (a *keeneticAuthenticator) Authenticate(ctx context.Context, login, password string) error {
	baseURL := a.baseURL
	if baseURL == "" {
		baseURL = "http://" + keeneticLANAddress()
	}
	authURL := baseURL + "/auth"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, authURL, nil)
	if err != nil {
		return err
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("keenetic auth challenge: %w", err)
	}
	challenge := strings.TrimSpace(resp.Header.Get("X-NDM-Challenge"))
	realm := strings.TrimSpace(resp.Header.Get("X-NDM-Realm"))
	cookies := resp.Cookies()
	_ = resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	if resp.StatusCode != http.StatusUnauthorized || challenge == "" || realm == "" {
		return fmt.Errorf("keenetic auth challenge returned status %d", resp.StatusCode)
	}

	payload, _ := json.Marshal(map[string]string{
		"login":    login,
		"password": keeneticPasswordHash(login, password, realm, challenge),
	})
	req, err = http.NewRequestWithContext(ctx, http.MethodPost, authURL, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}
	resp, err = a.client.Do(req)
	if err != nil {
		return fmt.Errorf("keenetic auth response: %w", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return errInvalidCredentials
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("keenetic auth returned status %d", resp.StatusCode)
	}
	return nil
}

func keeneticPasswordHash(login, password, realm, challenge string) string {
	md5Sum := md5.Sum([]byte(login + ":" + realm + ":" + password))
	shaSum := sha256.Sum256([]byte(challenge + hex.EncodeToString(md5Sum[:])))
	return hex.EncodeToString(shaSum[:])
}

func keeneticLANAddress() string {
	if iface, err := net.InterfaceByName("br0"); err == nil {
		if addrs, err := iface.Addrs(); err == nil {
			for _, addr := range addrs {
				ip, _, err := net.ParseCIDR(addr.String())
				if err == nil && ip.To4() != nil && !ip.IsLoopback() {
					return ip.String()
				}
			}
		}
	}
	return "127.0.0.1"
}

type authSession struct {
	Login     string
	ExpiresAt time.Time
}

type authAttempt struct {
	Failures   int
	BlockedTil time.Time
}

type authService struct {
	mu            sync.Mutex
	sessions      map[string]authSession
	attempts      map[string]authAttempt
	authenticator credentialAuthenticator
	now           func() time.Time
}

func newAuthService(authenticator credentialAuthenticator) *authService {
	return &authService{
		sessions:      make(map[string]authSession),
		attempts:      make(map[string]authAttempt),
		authenticator: authenticator,
		now:           time.Now,
	}
}

func (a *authService) createSession(login string, ttl time.Duration) (string, authSession, error) {
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", authSession{}, err
	}
	token := hex.EncodeToString(tokenBytes)
	session := authSession{Login: login, ExpiresAt: a.now().Add(ttl)}
	a.mu.Lock()
	a.sessions[token] = session
	a.mu.Unlock()
	return token, session, nil
}

func (a *authService) session(token string, ttl time.Duration) (authSession, bool) {
	if token == "" {
		return authSession{}, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	session, ok := a.sessions[token]
	if !ok || !session.ExpiresAt.After(a.now()) {
		delete(a.sessions, token)
		return authSession{}, false
	}
	// Sliding expiration keeps an actively used local admin session alive.
	session.ExpiresAt = a.now().Add(ttl)
	a.sessions[token] = session
	return session, true
}

func (a *authService) deleteSession(token string) {
	a.mu.Lock()
	delete(a.sessions, token)
	a.mu.Unlock()
}

func (a *authService) blocked(client string) (time.Duration, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	attempt := a.attempts[client]
	remaining := attempt.BlockedTil.Sub(a.now())
	if remaining > 0 {
		return remaining, true
	}
	if !attempt.BlockedTil.IsZero() {
		delete(a.attempts, client)
	}
	return 0, false
}

func (a *authService) failed(client string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	attempt := a.attempts[client]
	attempt.Failures++
	if attempt.Failures >= 5 {
		attempt.BlockedTil = a.now().Add(30 * time.Second)
		attempt.Failures = 0
	}
	a.attempts[client] = attempt
}

func (a *authService) succeeded(client string) {
	a.mu.Lock()
	delete(a.attempts, client)
	a.mu.Unlock()
}

func authClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}
