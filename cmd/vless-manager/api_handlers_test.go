package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func newHandlerTestAPI(t *testing.T) *apiServer {
	t.Helper()
	dir := t.TempDir()
	cfg := defaultConfig()
	cfg.AutoFailover = false
	cfg.Autostart = false
	return newAPIServer(
		NewProcessManager(dir),
		cfg,
		nil,
		filepath.Join(dir, "config.json"),
		filepath.Join(dir, "subscriptions.json"),
	)
}

func apiRequest(t *testing.T, api *apiServer, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, req)
	return rec
}

func TestReadOnlyAPIEndpoints(t *testing.T) {
	api := newHandlerTestAPI(t)
	api.pm.event(serviceLogInfo, "test", "ready", "ready")
	paths := []string{
		"/api/status",
		"/api/config",
		"/api/logs?since=0",
		"/api/servers",
		"/api/subscriptions",
		"/api/ping",
		"/api/traffic",
		"/api/version",
		"/api/failover",
		"/api/settings",
		"/api/settings/defaults",
		"/api/bypass",
	}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			rec := apiRequest(t, api, http.MethodGet, path, "")
			if rec.Code != http.StatusOK {
				t.Fatalf("%s: status=%d body=%s", path, rec.Code, rec.Body.String())
			}
			if !json.Valid(rec.Body.Bytes()) {
				t.Fatalf("%s returned invalid JSON: %s", path, rec.Body.String())
			}
		})
	}
}

func TestAPIRejectsUnsupportedMethods(t *testing.T) {
	api := newHandlerTestAPI(t)
	tests := []struct {
		method string
		path   string
	}{
		{http.MethodDelete, "/api/config"},
		{http.MethodGet, "/api/start"},
		{http.MethodGet, "/api/stop"},
		{http.MethodGet, "/api/restart"},
		{http.MethodPost, "/api/logs"},
		{http.MethodDelete, "/api/servers"},
		{http.MethodDelete, "/api/subscriptions"},
		{http.MethodDelete, "/api/ping"},
		{http.MethodGet, "/api/ping/auto-select"},
		{http.MethodPost, "/api/traffic"},
		{http.MethodGet, "/api/parse-uri"},
		{http.MethodGet, "/api/internet/check"},
		{http.MethodDelete, "/api/failover"},
		{http.MethodDelete, "/api/settings"},
		{http.MethodPost, "/api/settings/defaults"},
		{http.MethodDelete, "/api/bypass"},
	}
	for _, test := range tests {
		t.Run(test.method+" "+test.path, func(t *testing.T) {
			rec := apiRequest(t, api, test.method, test.path, "")
			if rec.Code != http.StatusMethodNotAllowed {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestTrafficDirections(t *testing.T) {
	tests := []struct {
		name             string
		iface            string
		rx, tx           uint64
		download, upload uint64
	}{
		{name: "Keenetic bridge", iface: "br0", rx: 100, tx: 900, download: 900, upload: 100},
		{name: "OpenWrt bridge", iface: "br-lan", rx: 200, tx: 800, download: 800, upload: 200},
		{name: "regular interface", iface: "tun0", rx: 700, tx: 300, download: 700, upload: 300},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			download, upload := trafficDirections(tt.iface, tt.rx, tt.tx)
			if download != tt.download || upload != tt.upload {
				t.Fatalf("trafficDirections(%q, %d, %d) = (%d, %d), want (%d, %d)",
					tt.iface, tt.rx, tt.tx, download, upload, tt.download, tt.upload)
			}
		})
	}
}

func TestTrafficAPIExposesSelectableModes(t *testing.T) {
	api := newHandlerTestAPI(t)
	rec := apiRequest(t, api, http.MethodGet, "/api/traffic", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		VPNRunning bool `json:"vpn_running"`
		Modes      map[string]struct {
			Available bool `json:"available"`
		} `json:"modes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Modes) != 3 {
		t.Fatalf("expected three traffic modes, got %+v", response.Modes)
	}
	if !response.Modes["all"].Available {
		t.Fatal("all traffic must always be available")
	}
	if response.VPNRunning || response.Modes["vpn"].Available || response.Modes["bypass"].Available {
		t.Fatalf("VPN-specific modes must be unavailable while VPN is stopped: %+v", response)
	}
}

func TestSettingsAPIValidationAndPersistence(t *testing.T) {
	api := newHandlerTestAPI(t)
	valid := `{
		"ping_selection_mode":"fastest",
		"ping_failover_order":"priority"
	}`
	rec := apiRequest(t, api, http.MethodPatch, "/api/settings", valid)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid patch: status=%d body=%s", rec.Code, rec.Body.String())
	}
	got := api.settingsSnapshot()
	if got.PingSelectionMode != "fastest" || got.PingUseFreshCache ||
		got.PingFailoverOrder != "priority" {
		t.Fatalf("settings were not applied: %+v", got)
	}
	loaded, err := loadConfig(api.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Settings.PingUseFreshCache {
		t.Fatal("ping result reuse must remain disabled")
	}

	for name, body := range map[string]string{
		"malformed": `{"ping_selection_mode":`,
		"unknown":   `{"unknown_setting":1}`,
		"invalid":   `{"ping_max_parallel":3}`,
	} {
		t.Run(name, func(t *testing.T) {
			rec := apiRequest(t, api, http.MethodPatch, "/api/settings", body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestConfigAPIValidationAndRoundTrip(t *testing.T) {
	api := newHandlerTestAPI(t)
	rec := apiRequest(t, api, http.MethodPost, "/api/config", "{")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed status=%d", rec.Code)
	}

	cfg := defaultConfig()
	cfg.Port = 0
	cfg.AutoFailover = false
	cfg.Autostart = false
	cfg.Settings.PingTimeoutSec = 2
	data, _ := json.Marshal(cfg)
	rec = apiRequest(t, api, http.MethodPost, "/api/config", string(data))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid status=%d body=%s", rec.Code, rec.Body.String())
	}

	cfg.Settings = defaultSettings()
	cfg.Settings.PingSelectionMode = "fastest"
	data, _ = json.Marshal(cfg)
	rec = apiRequest(t, api, http.MethodPost, "/api/config", string(data))
	if rec.Code != http.StatusOK {
		t.Fatalf("valid status=%d body=%s", rec.Code, rec.Body.String())
	}
	if api.cfg.Port != 3001 || api.cfg.Settings.PingSelectionMode != "fastest" {
		t.Fatalf("unexpected config: %+v", api.cfg)
	}
}

func TestManualServerCRUDAndConnectStartsVPN(t *testing.T) {
	api := newHandlerTestAPI(t)
	starts := 0
	api.connectStartFn = func(*Config) error {
		starts++
		return nil
	}

	for name, body := range map[string]string{
		"malformed": "{",
		"required":  `{"name":"missing"}`,
		"transport": `{"address":"example.com","uuid":"u","network":"kcp"}`,
	} {
		t.Run(name, func(t *testing.T) {
			rec := apiRequest(t, api, http.MethodPost, "/api/servers", body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}

	rec := apiRequest(t, api, http.MethodPost, "/api/servers",
		`{"address":"example.com","port":443,"uuid":"u","security":"tls"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", rec.Code, rec.Body.String())
	}
	var created VLESSServer
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.Name != "example.com" || !created.Manual ||
		created.Network != "tcp" || created.Fingerprint != "chrome" {
		t.Fatalf("created=%+v", created)
	}

	if rec = apiRequest(t, api, http.MethodGet, "/api/servers/"+created.ID, ""); rec.Code != http.StatusOK {
		t.Fatalf("get status=%d", rec.Code)
	}
	if rec = apiRequest(t, api, http.MethodPost, "/api/servers/"+created.ID+"/connect", ""); rec.Code != http.StatusOK {
		t.Fatalf("connect status=%d body=%s", rec.Code, rec.Body.String())
	}
	if api.cfg.ActiveServer != created.ID {
		t.Fatalf("active=%q", api.cfg.ActiveServer)
	}
	if starts != 1 {
		t.Fatalf("connect starts=%d, want 1", starts)
	}

	update := created
	update.Name = "updated"
	data, _ := json.Marshal(update)
	if rec = apiRequest(t, api, http.MethodPut, "/api/servers/"+created.ID, string(data)); rec.Code != http.StatusOK {
		t.Fatalf("update status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec = apiRequest(t, api, http.MethodDelete, "/api/servers/"+created.ID, ""); rec.Code != http.StatusOK {
		t.Fatalf("delete status=%d", rec.Code)
	}
	if api.cfg.ActiveServer != "" || len(api.cfg.Servers) != 0 {
		t.Fatalf("server not deleted: active=%q servers=%v", api.cfg.ActiveServer, api.cfg.Servers)
	}
	if rec = apiRequest(t, api, http.MethodGet, "/api/servers/missing", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("missing status=%d", rec.Code)
	}
}

func TestParseURIAndPingSelectionHandlers(t *testing.T) {
	api := newHandlerTestAPI(t)
	if rec := apiRequest(t, api, http.MethodPost, "/api/parse-uri", "{"); rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed parse status=%d", rec.Code)
	}
	if rec := apiRequest(t, api, http.MethodPost, "/api/parse-uri", `{"uri":"bad"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid URI status=%d", rec.Code)
	}
	uri := "vless://11111111-1111-1111-1111-111111111111@example.com:443?security=tls&type=tcp&sni=example.com#test"
	if rec := apiRequest(t, api, http.MethodPost, "/api/parse-uri", `{"uri":"`+uri+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("valid URI status=%d body=%s", rec.Code, rec.Body.String())
	}

	if rec := apiRequest(t, api, http.MethodPost, "/api/ping", ""); rec.Code != http.StatusOK || rec.Body.String() != "[]\n" {
		t.Fatalf("empty ping status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := apiRequest(t, api, http.MethodPost, "/api/ping/auto-select", ""); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("empty auto select status=%d body=%s", rec.Code, rec.Body.String())
	}

	server := VLESSServer{ID: "working", Name: "working", Address: "example.com", Port: 443, UUID: "u", Network: "tcp"}
	api.subs = []*Subscription{{ID: "sub", Servers: []VLESSServer{server}}}
	api.connectStartFn = func(*Config) error { return nil }
	api.pingRunner = func(servers []VLESSServer) []PingResult {
		return []PingResult{{ServerID: server.ID, ServerName: server.Name, LatencyMS: 12}}
	}
	rec := apiRequest(t, api, http.MethodPost, "/api/ping/auto-select", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("auto select status=%d body=%s", rec.Code, rec.Body.String())
	}
	if api.cfg.ActiveServer != server.ID {
		t.Fatalf("active=%q", api.cfg.ActiveServer)
	}
}

func TestSafeProcessAndFailoverHandlers(t *testing.T) {
	api := newHandlerTestAPI(t)
	if rec := apiRequest(t, api, http.MethodPost, "/api/stop", ""); rec.Code != http.StatusOK {
		t.Fatalf("stop status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := apiRequest(t, api, http.MethodPost, "/api/start", ""); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("start status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := apiRequest(t, api, http.MethodPost, "/api/restart", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("restart status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := apiRequest(t, api, http.MethodPost, "/api/failover", "{"); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad toggle status=%d", rec.Code)
	}
	if rec := apiRequest(t, api, http.MethodPost, "/api/failover", `{}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty toggle status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := apiRequest(t, api, http.MethodPost, "/api/failover", `{"enabled":false}`); rec.Code != http.StatusOK {
		t.Fatalf("toggle status=%d body=%s", rec.Code, rec.Body.String())
	}
	if api.cfg.AutoFailover {
		t.Fatal("operator automation was not disabled")
	}
	if rec := apiRequest(t, api, http.MethodPost, "/api/failover", `{"tunnel_failover_enabled":false}`); rec.Code != http.StatusOK {
		t.Fatalf("tunnel toggle status=%d body=%s", rec.Code, rec.Body.String())
	}
	if api.cfg.AutoTunnelFailover {
		t.Fatal("tunnel monitoring was not disabled")
	}
}

func TestWriteJSONAndErrorHelpers(t *testing.T) {
	rec := httptest.NewRecorder()
	writeJSON(rec, map[string]int{"ok": 1})
	if rec.Code != http.StatusOK || !bytes.Contains(rec.Body.Bytes(), []byte(`"ok":1`)) {
		t.Fatalf("writeJSON: status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	writeError(rec, http.StatusTeapot, "no")
	if rec.Code != http.StatusTeapot || !bytes.Contains(rec.Body.Bytes(), []byte(`"error":"no"`)) {
		t.Fatalf("writeError: status=%d body=%s", rec.Code, rec.Body.String())
	}
}
