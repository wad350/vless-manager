package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fakeCredentialAuthenticator struct {
	err   error
	calls *int
}

func (f fakeCredentialAuthenticator) Authenticate(context.Context, string, string) error {
	if f.calls != nil {
		*f.calls++
	}
	return f.err
}

func TestKeeneticPasswordHash(t *testing.T) {
	got := keeneticPasswordHash("admin", "secret", "Keenetic", "challenge")
	want := "8382529cb2669c562423a56a18499345c314a09cf530fbc6ea4e0f7075dff662"
	if got != want {
		t.Fatalf("hash = %q, want %q", got, want)
	}
}

func TestKeeneticAuthenticatorChallengeFlow(t *testing.T) {
	const sessionCookie = "test-session"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("X-NDM-Challenge", "challenge")
			w.Header().Set("X-NDM-Realm", "Keenetic")
			http.SetCookie(w, &http.Cookie{Name: "ndm_session", Value: sessionCookie})
			w.WriteHeader(http.StatusUnauthorized)
		case http.MethodPost:
			cookie, err := r.Cookie("ndm_session")
			if err != nil || cookie.Value != sessionCookie {
				t.Errorf("challenge cookie not forwarded: cookie=%v err=%v", cookie, err)
			}
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body["login"] != "admin" || body["password"] != keeneticPasswordHash("admin", "secret", "Keenetic", "challenge") {
				t.Errorf("unexpected auth payload: %+v", body)
			}
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()

	authenticator := newKeeneticAuthenticator(server.URL)
	if err := authenticator.Authenticate(context.Background(), "admin", "secret"); err != nil {
		t.Fatal(err)
	}
}

func TestAuthSessionUsesSlidingExpiration(t *testing.T) {
	auth := newAuthService(fakeCredentialAuthenticator{})
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	auth.now = func() time.Time { return now }
	token, created, err := auth.createSession("admin", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if created.ExpiresAt != now.Add(time.Hour) {
		t.Fatalf("initial expiry = %s", created.ExpiresAt)
	}
	now = now.Add(30 * time.Minute)
	session, ok := auth.session(token, time.Hour)
	if !ok || session.ExpiresAt != now.Add(time.Hour) {
		t.Fatalf("session was not extended: ok=%v expiry=%s", ok, session.ExpiresAt)
	}
	now = now.Add(61 * time.Minute)
	if _, ok := auth.session(token, time.Hour); ok {
		t.Fatal("expired session remained valid")
	}
}

func TestAuthThrottleBlocksFifthFailure(t *testing.T) {
	auth := newAuthService(fakeCredentialAuthenticator{})
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	auth.now = func() time.Time { return now }
	for i := 0; i < 4; i++ {
		auth.failed("192.0.2.1")
		if _, blocked := auth.blocked("192.0.2.1"); blocked {
			t.Fatalf("blocked after only %d failures", i+1)
		}
	}
	auth.failed("192.0.2.1")
	if remaining, blocked := auth.blocked("192.0.2.1"); !blocked || remaining != 30*time.Second {
		t.Fatalf("fifth failure did not block: blocked=%v remaining=%s", blocked, remaining)
	}
}

func TestAuthMiddlewareAndLogin(t *testing.T) {
	api := newHandlerTestAPI(t)
	api.cfg.Settings.AuthEnabled = true
	api.auth = newAuthService(fakeCredentialAuthenticator{})

	unauthorized := apiRequest(t, api, http.MethodGet, "/api/status", "")
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("protected API status=%d body=%s", unauthorized.Code, unauthorized.Body.String())
	}
	publicStatus := apiRequest(t, api, http.MethodGet, "/api/auth/status", "")
	if publicStatus.Code != http.StatusOK || !strings.Contains(publicStatus.Body.String(), `"authenticated":false`) {
		t.Fatalf("auth status=%d body=%s", publicStatus.Code, publicStatus.Body.String())
	}

	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"login":"admin","password":"secret"}`))
	req.RemoteAddr = "192.0.2.10:12345"
	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", rec.Code, rec.Body.String())
	}
	response := rec.Result()
	var sessionCookie *http.Cookie
	for _, cookie := range response.Cookies() {
		if cookie.Name == authCookieName {
			sessionCookie = cookie
		}
	}
	if sessionCookie == nil || !sessionCookie.HttpOnly || sessionCookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("secure session cookie not returned: %+v", sessionCookie)
	}

	authedReq := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	authedReq.AddCookie(sessionCookie)
	authedRec := httptest.NewRecorder()
	api.ServeHTTP(authedRec, authedReq)
	if authedRec.Code != http.StatusOK {
		t.Fatalf("authenticated API status=%d body=%s", authedRec.Code, authedRec.Body.String())
	}
}

func TestAuthLoginRejectsInvalidCredentials(t *testing.T) {
	api := newHandlerTestAPI(t)
	api.cfg.Settings.AuthEnabled = true
	calls := 0
	api.auth = newAuthService(fakeCredentialAuthenticator{err: errInvalidCredentials, calls: &calls})
	rec := apiRequest(t, api, http.MethodPost, "/api/auth/login", `{"login":"admin","password":"wrong"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !errors.Is(api.auth.authenticator.(fakeCredentialAuthenticator).err, errInvalidCredentials) {
		t.Fatal("fake authenticator was not configured")
	}
	if calls != 1 {
		t.Fatalf("invalid credentials were retried %d times", calls)
	}
}
