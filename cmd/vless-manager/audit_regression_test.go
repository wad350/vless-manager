package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestAcquireCancellationDoesNotRetainOperationSlot(t *testing.T) {
	c := testCoordinator(t)
	for i := 0; i < 50; i++ {
		blockStarted := make(chan struct{})
		releaseBlock := make(chan struct{})
		blockDone := make(chan error, 1)
		go func() {
			blockDone <- c.Run(context.Background(), operationRequest{Kind: "block"},
				func(context.Context, func(operationProgress)) error {
					close(blockStarted)
					<-releaseBlock
					return nil
				})
		}()
		<-blockStarted

		ctx, cancel := context.WithCancel(context.Background())
		acquireDone := make(chan error, 1)
		go func() {
			lease, err := c.Acquire(ctx, operationRequest{Kind: "cancelled", Cancellable: true})
			if lease != nil {
				lease.Finish(nil)
			}
			acquireDone <- err
		}()
		cancel()
		close(releaseBlock)
		if err := <-blockDone; err != nil {
			t.Fatal(err)
		}
		if err := <-acquireDone; !errors.Is(err, context.Canceled) {
			t.Fatalf("Acquire error = %v, want context cancellation", err)
		}

		nextDone := make(chan error, 1)
		go func() {
			nextDone <- c.Run(context.Background(), operationRequest{Kind: "next"},
				func(context.Context, func(operationProgress)) error { return nil })
		}()
		select {
		case err := <-nextDone:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("cancelled Acquire retained the global operation slot")
		}
	}
}

func TestGlobalRouteReadyRequiresPolicyRulesAndRouteOutput(t *testing.T) {
	command := func(name string, args ...string) (string, error) {
		joined := name + " " + strings.Join(args, " ")
		switch joined {
		case "ip rule show":
			return "1: from all fwmark 0x1 lookup 100\n2: from all fwmark 0x9911 lookup main\n", nil
		case "ip route show table 100":
			return "default dev tun0 scope link\n", nil
		default:
			return "ok", nil
		}
	}
	if !globalRouteReady(command) {
		t.Fatal("complete route state was rejected")
	}

	missingRule := func(name string, args ...string) (string, error) {
		if name == "ip" && strings.Join(args, " ") == "rule show" {
			return "2: from all fwmark 0x9911 lookup main\n", nil
		}
		return command(name, args...)
	}
	if globalRouteReady(missingRule) {
		t.Fatal("route state without tunnel policy rule was accepted")
	}

	emptyRoute := func(name string, args ...string) (string, error) {
		if name == "ip" && strings.Join(args, " ") == "route show table 100" {
			return "", nil
		}
		return command(name, args...)
	}
	if globalRouteReady(emptyRoute) {
		t.Fatal("empty policy route output was accepted")
	}
}

func TestSubscriptionFetchHonorsContextCancellation(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		<-req.Context().Done()
		return nil, req.Context().Err()
	})}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := fetchSubscriptionWithClientContext(ctx, "https://example.com/sub", client)
		done <- err
	}()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("fetch error = %v, want context cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("subscription request ignored context cancellation")
	}
}

func TestMalformedSubscriptionNodesAreReported(t *testing.T) {
	body := strings.Join([]string{
		"vless://not-a-valid-uri",
		"vless://00000000-0000-0000-0000-000000000002@example.com:443?type=ws&security=tls#valid",
	}, "\n")
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: closeString(body), Header: make(http.Header)}, nil
	})}
	sub, err := fetchSubscriptionWithClient("https://example.com/sub", client)
	if err != nil {
		t.Fatal(err)
	}
	if len(sub.Servers) != 1 || sub.ExcludedServers != 1 || sub.ExcludedTransports["invalid"] != 1 {
		t.Fatalf("servers=%d excluded=%d reasons=%v", len(sub.Servers), sub.ExcludedServers, sub.ExcludedTransports)
	}
}

func TestCloneConfigIsDeep(t *testing.T) {
	cfg := defaultConfig()
	cfg.Servers = []VLESSServer{{Name: "group", Members: []VLESSServer{{Name: "member"}}, XHTTPHeaders: map[string]string{"A": "one"}}}
	cfg.Settings.OpenProbes = []string{"https://one"}
	cfg.BypassCache.Domains = []string{"one.example"}
	clone := cloneConfig(cfg)
	clone.Servers[0].Members[0].Name = "changed"
	clone.Servers[0].XHTTPHeaders["A"] = "changed"
	clone.Settings.OpenProbes[0] = "changed"
	clone.BypassCache.Domains[0] = "changed"
	if got := fmt.Sprint(cfg.Servers[0].Members[0].Name, cfg.Servers[0].XHTTPHeaders["A"], cfg.Settings.OpenProbes[0], cfg.BypassCache.Domains[0]); got != "memberonehttps://oneone.example" {
		t.Fatalf("source config was mutated through clone: %s", got)
	}
}

func TestPingHTTPStatusRejectsErrors(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusNotFound, http.StatusInternalServerError, http.StatusBadGateway} {
		if pingHTTPStatusOK(status) {
			t.Fatalf("HTTP %d accepted as a healthy tunnel", status)
		}
	}
	for _, status := range []int{http.StatusOK, http.StatusNoContent, http.StatusFound} {
		if !pingHTTPStatusOK(status) {
			t.Fatalf("HTTP %d rejected", status)
		}
	}
}

func TestPersistMutationRestoresMemoryOnWriteFailure(t *testing.T) {
	previous := defaultConfig()
	previous.Port = 3001
	s := &apiServer{
		pm:      NewProcessManager(t.TempDir()),
		cfg:     cloneConfig(previous),
		cfgPath: t.TempDir(), // rename over a directory must fail
	}
	s.cfg.Port = 9999
	if err := s.persistMutationLocked(cloneConfig(previous), nil, true, false); err == nil {
		t.Fatal("expected persistence failure")
	}
	if s.cfg.Port != previous.Port {
		t.Fatalf("in-memory config was not restored: port=%d", s.cfg.Port)
	}
}

func TestAuthStateIsBoundedAndExpiredEntriesAreCleaned(t *testing.T) {
	now := time.Now()
	auth := newAuthService(nil)
	auth.now = func() time.Time { return now }
	for i := 0; i < maxAuthSessions+20; i++ {
		if _, _, err := auth.createSession(fmt.Sprintf("user-%d", i), time.Hour); err != nil {
			t.Fatal(err)
		}
	}
	if len(auth.sessions) != maxAuthSessions {
		t.Fatalf("sessions=%d, want %d", len(auth.sessions), maxAuthSessions)
	}
	for i := 0; i < maxAuthAttempts+50; i++ {
		auth.failed(fmt.Sprintf("192.0.2.%d", i))
	}
	if len(auth.attempts) > maxAuthAttempts {
		t.Fatalf("attempts grew past cap: %d", len(auth.attempts))
	}
	now = now.Add(11 * time.Minute)
	_, _ = auth.blocked("cleanup-trigger")
	if len(auth.attempts) != 0 {
		t.Fatalf("stale attempts were not cleaned: %d", len(auth.attempts))
	}
}

type stringReadCloser struct{ *strings.Reader }

func (stringReadCloser) Close() error { return nil }

func closeString(value string) stringReadCloser {
	return stringReadCloser{Reader: strings.NewReader(value)}
}
