package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAuditRefreshPreservesConcurrentSubscriptionOptions(t *testing.T) {
	for _, background := range []bool{false, true} {
		t.Run(map[bool]string{false: "manual", true: "background"}[background], func(t *testing.T) {
			for _, rotate := range []bool{false, true} {
				t.Run(map[bool]string{false: "disable", true: "reenable_rotated_credentials"}[rotate], func(t *testing.T) {
					auditRefreshOptionsDuringFetch(t, background, rotate)
				})
			}
		})
	}
}

func auditRefreshOptionsDuringFetch(t *testing.T, background, rotate bool) {
	t.Helper()
	originalURI := "vless://11111111-1111-1111-1111-111111111111@example.test:443?security=tls&type=tcp#original"
	freshURI := originalURI
	if rotate {
		freshURI = strings.Replace(originalURI, "11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222", 1)
	}
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(freshURI))
	}))
	defer backend.Close()
	defer unblock()
	oldID := subscriptionDeviceID
	subscriptionDeviceID = strings.Repeat("a", 64)
	defer func() { subscriptionDeviceID = oldID }()
	server, err := parseVLESSURI(originalURI)
	if err != nil {
		t.Fatal(err)
	}
	freshServer, err := parseVLESSURI(freshURI)
	if err != nil {
		t.Fatal(err)
	}
	api := newHandlerTestAPI(t)
	auditRefreshSOCKSBackend(t, strings.TrimPrefix(backend.URL, "http://"))
	api.pm.running = true
	api.failover.state.VPNHealthOK = true
	api.subs = []*Subscription{{ID: "audit-sub", Name: "before", URL: backend.URL, Servers: []VLESSServer{*server}}}
	if rotate {
		api.subs[0].DisabledServerIDs = []string{server.ID}
	}
	done := make(chan error, 1)
	go func() {
		if background {
			done <- api.refreshAllSubscriptionsCore(context.Background(), func(operationProgress) {})
			return
		}
		request := httptest.NewRequest(http.MethodPost, "/api/subscriptions/audit-sub/refresh", nil)
		result := httptest.NewRecorder()
		api.ServeHTTP(result, request)
		if result.Code != http.StatusOK {
			done <- fmt.Errorf("refresh HTTP %d: %s", result.Code, result.Body.String())
			return
		}
		done <- nil
	}()
	select {
	case <-started:
	case err := <-done:
		t.Fatalf("refresh ended before backend: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("refresh never reached isolated backend")
	}
	disabled := !rotate
	body := fmt.Sprintf(`{"name":"edited while loading","disabled":%t}`, disabled)
	patch := apiRequest(t, api, http.MethodPatch, "/api/subscriptions/audit-sub", body)
	if patch.Code != http.StatusOK {
		t.Fatalf("PATCH status=%d", patch.Code)
	}
	nodePatch := apiRequest(t, api, http.MethodPatch, "/api/subscriptions/audit-sub/servers/"+server.ID, fmt.Sprintf(`{"disabled":%t}`, disabled))
	if nodePatch.Code != http.StatusOK {
		t.Fatalf("node PATCH status=%d", nodePatch.Code)
	}
	unblock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("refresh: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("refresh did not complete")
	}
	api.mu.RLock()
	sub := cloneSubscription(api.subs[0])
	api.mu.RUnlock()
	if sub.Name != "edited while loading" || sub.Disabled != disabled || sub.serverDisabled(freshServer.ID) != disabled {
		t.Fatalf("refresh overwrote concurrent user choices: name=%q disabled=%v nodeDisabled=%v", sub.Name, sub.Disabled, sub.serverDisabled(freshServer.ID))
	}
	if len(sub.Servers) != 1 || sub.Servers[0].UUID != freshServer.UUID {
		t.Fatal("fresh catalog credentials were discarded")
	}
	expectedDisabledIDs := 0
	if disabled {
		expectedDisabledIDs = 1
	}
	if len(sub.DisabledServerIDs) != expectedDisabledIDs {
		t.Fatalf("disabled IDs=%v", sub.DisabledServerIDs)
	}
}

// Keep the HTTP path real while avoiding the Linux-specific SO_MARK dial on
// Darwin. This proxy can forward only to this test's loopback HTTP backend.
func auditRefreshSOCKSBackend(t *testing.T, target string) {
	t.Helper()
	listener, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", socksHealthPort))
	if err != nil {
		t.Fatalf("isolated SOCKS health port unavailable: %v", err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		greeting := make([]byte, 3)
		if _, err := io.ReadFull(conn, greeting); err != nil {
			return
		}
		if greeting[0] != 5 || greeting[1] != 1 || greeting[2] != 0 {
			t.Errorf("unexpected SOCKS greeting: %v", greeting)
			return
		}
		if _, err := conn.Write([]byte{5, 0}); err != nil {
			return
		}
		request := make([]byte, 10)
		if _, err := io.ReadFull(conn, request); err != nil {
			return
		}
		if request[0] != 5 || request[1] != 1 || request[3] != 1 {
			t.Errorf("unexpected SOCKS request: %v", request)
			return
		}
		upstream, err := net.DialTimeout("tcp4", target, time.Second)
		if err != nil {
			t.Errorf("isolated backend dial: %v", err)
			return
		}
		defer upstream.Close()
		if _, err := conn.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0}); err != nil {
			return
		}
		copied := make(chan struct{})
		go func() { _, _ = io.Copy(upstream, conn); _ = upstream.Close(); close(copied) }()
		_, _ = io.Copy(conn, upstream)
		_ = conn.Close()
		<-copied
	}()
}
