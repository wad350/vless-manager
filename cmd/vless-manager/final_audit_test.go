package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHealthSOCKSInboundCannotBypassBrokenTunnel(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()
	cfg := defaultConfig()
	cfg.Settings.BypassDomains = []string{"localhost"}
	server := &VLESSServer{Address: "127.0.0.1", Port: unusedTCPPort(t), UUID: integrationUUID, Network: "tcp"}
	data, err := generateXrayConfig(cfg, server)
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	port := unusedTCPPort(t)
	config["inbounds"] = []any{xraySOCKSInbound(port)}
	for _, raw := range config["outbounds"].([]any) {
		delete(raw.(map[string]any)["streamSettings"].(map[string]any)["sockopt"].(map[string]any), "mark")
	}
	engine := startTestXray(t, config, false)
	result := pingViaSOCKSContext(context.Background(), server, port, 300*time.Millisecond,
		strings.Replace(target.URL, "127.0.0.1", "localhost", 1))
	if result.LatencyMS >= 0 || engine.TrafficSnapshot().BypassDownload > 0 {
		t.Fatalf("broken tunnel was reported healthy through bypass: %+v, traffic=%+v", result, engine.TrafficSnapshot())
	}
}

func TestSOCKSProbeCancellationClosesHandshake(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	closed := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			closed <- err
			return
		}
		defer conn.Close()
		accepted <- conn
		_, err = io.Copy(io.Discard, conn)
		closed <- err
	}()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan PingResult, 1)
	go func() {
		finished <- pingViaSOCKSContext(ctx, &VLESSServer{}, listener.Addr().(*net.TCPAddr).Port,
			5*time.Second, "http://example.test/")
	}()
	var conn net.Conn
	select {
	case conn = <-accepted:
		defer conn.Close()
	case <-time.After(time.Second):
		t.Fatal("SOCKS handshake never started")
	}
	cancel()
	select {
	case result := <-finished:
		if result.Error == "" {
			t.Fatal("cancelled probe succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("probe ignored cancellation")
	}
	select {
	case <-closed:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("cancelled HTTP request retained its SOCKS socket and dial goroutine")
	}
}

func TestLoadSubscriptionsRejectsNullEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "subscriptions.json")
	if err := os.WriteFile(path, []byte(`[null]`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSubscriptions(path); err == nil {
		t.Fatal("null subscription entry accepted")
	}
}

func TestStartupReadFailurePreservesSavedState(t *testing.T) {
	dir := t.TempDir()
	cfgPath, subPath := filepath.Join(dir, "config.json"), filepath.Join(dir, "subscriptions.json")
	cfg := defaultConfig()
	cfg.ActiveServer = "selected"
	cfg.Servers = []VLESSServer{{ID: "selected", Address: "example.com", Port: 443, UUID: integrationUUID}}
	if err := saveConfig(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{`{invalid`, `[null]`} {
		if err := os.WriteFile(subPath, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if cfg, subs, err := loadStartupState(cfgPath, subPath); err == nil || cfg != nil || subs != nil {
			t.Fatalf("failed startup exposed state for pruning: cfg=%v subs=%v err=%v", cfg, subs, err)
		}
		after, _ := os.ReadFile(cfgPath)
		original, _ := os.ReadFile(subPath)
		if string(before) != string(after) || string(original) != body {
			t.Fatal("failed startup rewrote persistent state")
		}
	}
}

func TestHealthDisableDuringProbeDiscardsResult(t *testing.T) {
	_, fc := newFailoverTestController(t)
	fc.statusFn = func() ProcessStatus { return ProcessStatus{Running: true} }
	fc.vpnProbeFn = func(string, time.Duration) (bool, error) {
		fc.SetTunnelFailoverEnabled(false)
		return true, nil
	}
	fc.healthTick()
	state := fc.State()
	if state.VPNHealthOK || !state.VPNHealthCheck.IsZero() {
		t.Fatalf("disabled monitoring published an in-flight result: %+v", state)
	}
}

func TestHealthResultBelongsToProbedTunnelGeneration(t *testing.T) {
	_, fc := newFailoverTestController(t)
	generation := uint64(1)
	fc.statusFn = func() ProcessStatus { return ProcessStatus{Running: true, Generation: generation} }
	fc.vpnProbeFn = func(string, time.Duration) (bool, error) {
		generation++
		return true, nil
	}
	fc.healthTick()
	if state := fc.State(); state.VPNHealthOK || !state.VPNHealthCheck.IsZero() {
		t.Fatalf("new tunnel inherited old tunnel's health: %+v", state)
	}
	fc.vpnProbeFn = func(string, time.Duration) (bool, error) { return true, nil }
	fc.healthTick()
	if !fc.State().VPNHealthOK {
		t.Fatal("current tunnel's successful probe was discarded")
	}
	generation++
	if fc.State().VPNHealthOK {
		t.Fatal("unprobed tunnel reused a prior successful check")
	}
}

func TestFailoverStopIsIdempotentBeforeRunExits(t *testing.T) {
	_, fc := newFailoverTestController(t)
	fc.running = true
	fc.Stop()
	fc.Stop()
}

func TestAPIRejectsCrossOriginMutationsWithoutAuthentication(t *testing.T) {
	api := newHandlerTestAPI(t)
	req := httptest.NewRequest(http.MethodPatch, "http://192.168.201.1:3001/api/settings",
		strings.NewReader(`{"service_log_level":"trace"}`))
	req.Header.Set("Origin", "https://attacker.example")
	req.Header.Set("Content-Type", "text/plain")
	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || api.settingsSnapshot().ServiceLogLevel == "trace" {
		t.Fatalf("cross-origin mutation accepted: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAPIAllowsSameOriginAndNonBrowserRequests(t *testing.T) {
	for _, headers := range []map[string]string{
		nil,
		{"Origin": "http://192.168.201.1:3001"},
		{"Sec-Fetch-Site": "same-origin"},
	} {
		api := newHandlerTestAPI(t)
		req := httptest.NewRequest(http.MethodPatch, "http://192.168.201.1:3001/api/settings",
			strings.NewReader(`{"service_log_level":"trace"}`))
		for key, value := range headers {
			req.Header.Set(key, value)
		}
		rec := httptest.NewRecorder()
		api.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("valid request rejected: headers=%v status=%d", headers, rec.Code)
		}
	}
}

func TestPersistedCredentialsAreOwnerOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := saveConfig(path, defaultConfig()); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("credential file permissions: info=%v err=%v", info, err)
	}
}

func TestLoadConfigRejectsInvalidSavedSettings(t *testing.T) {
	for _, body := range []string{
		`{"port":-1}`,
		`{"port":65536}`,
		`{"settings":{"failover_outer_interval_sec":3601}}`,
		`{"settings":{"ping_test_url":"file:///etc/passwd"}}`,
	} {
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadConfig(path); err == nil {
			t.Fatalf("invalid saved state accepted: %s", body)
		}
	}
}

func TestScheduledUpdateRejectsAnotherInstallOrCheck(t *testing.T) {
	api := newHandlerTestAPI(t)
	api.updater.finish(UpdateStatus{State: "restarting"})
	if api.updater.begin("checking") {
		t.Fatal("scheduled installation allowed a concurrent update/check")
	}
	if status, err := api.updater.startInstall(); err == nil || status.State != "restarting" {
		t.Fatalf("duplicate installation accepted: status=%+v err=%v", status, err)
	}
}

func TestDiagnosticsLoginRequiresCredentialsWhenUIAuthIsDisabled(t *testing.T) {
	api := newHandlerTestAPI(t)
	api.diagnosticsEnabled = true
	calls := 0
	api.auth = newAuthService(fakeCredentialAuthenticator{calls: &calls})
	req := httptest.NewRequest(http.MethodGet, "/api/diagnostics/runtime", nil)
	req.RemoteAddr = "192.168.201.62:12345"
	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("diagnostics did not require a session: %d", rec.Code)
	}
	login := apiRequest(t, api, http.MethodPost, "/api/auth/login", `{"login":"admin","password":"secret"}`)
	cookies := login.Result().Cookies()
	if login.Code != http.StatusOK || calls != 1 || len(cookies) != 1 {
		t.Fatalf("diagnostics login did not authenticate: status=%d calls=%d cookies=%v", login.Code, calls, cookies)
	}
	req.AddCookie(cookies[0])
	rec = httptest.NewRecorder()
	api.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("authenticated diagnostics rejected: %d %s", rec.Code, rec.Body.String())
	}
	api.auth = newAuthService(fakeCredentialAuthenticator{err: errInvalidCredentials})
	login = apiRequest(t, api, http.MethodPost, "/api/auth/login", `{"login":"admin","password":"wrong"}`)
	if login.Code != http.StatusUnauthorized || len(login.Result().Cookies()) != 0 {
		t.Fatal("diagnostics login accepted invalid credentials")
	}
}
