package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSubscriptionDeviceIDIsStableAndHardwareBound(t *testing.T) {
	oldPaths := routerIdentityPaths
	oldID := subscriptionDeviceID
	t.Cleanup(func() {
		routerIdentityPaths = oldPaths
		subscriptionDeviceID = oldID
	})

	identityPath := filepath.Join(t.TempDir(), "router-mac")
	if err := os.WriteFile(identityPath, []byte("02:11:22:33:44:55\n"), 0600); err != nil {
		t.Fatal(err)
	}
	routerIdentityPaths = []string{identityPath}

	firstDir := t.TempDir()
	if err := initializeSubscriptionDeviceID(firstDir); err != nil {
		t.Fatal(err)
	}
	first := subscriptionDeviceID
	if !validSubscriptionDeviceID(first) || strings.Contains(first, "02:11:22") {
		t.Fatalf("invalid or raw device ID %q", first)
	}

	subscriptionDeviceID = ""
	if err := initializeSubscriptionDeviceID(firstDir); err != nil {
		t.Fatal(err)
	}
	if subscriptionDeviceID != first {
		t.Fatalf("persisted ID changed: %q != %q", subscriptionDeviceID, first)
	}

	if err := os.WriteFile(identityPath, []byte("02:aa:bb:cc:dd:ee\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := initializeSubscriptionDeviceID(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if subscriptionDeviceID == first {
		t.Fatal("different router identity produced the same X-Hwid")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestSubscriptionPreferVPNSettingRoundTrip(t *testing.T) {
	input := defaultSettings()
	input.SubscriptionPreferVPN = true
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	var output AppSettings
	if err := json.Unmarshal(data, &output); err != nil {
		t.Fatal(err)
	}
	if !output.SubscriptionPreferVPN {
		t.Fatal("subscription_prefer_vpn was lost during JSON round-trip")
	}
}

func TestFetchSubscriptionRejectsHTTPError(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusForbidden,
			Body:       io.NopCloser(strings.NewReader("blocked")),
			Header:     make(http.Header),
		}, nil
	})}
	_, err := fetchSubscriptionWithClient("https://example.com/sub", client)
	if err == nil {
		t.Fatal("expected HTTP error")
	}
}

func TestFetchSubscriptionSendsStableDeviceHeaders(t *testing.T) {
	body := "vless://00000000-0000-0000-0000-000000000002@example.com:443?type=ws&security=tls#supported"
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if got := req.Header.Get("X-Hwid"); got != subscriptionDeviceID {
			t.Fatalf("X-Hwid = %q, want %q", got, subscriptionDeviceID)
		}
		if got := req.Header.Get("X-Device-Os"); got != "Linux" {
			t.Fatalf("X-Device-Os = %q, want Linux", got)
		}
		if got := req.Header.Get("X-Device-Model"); got != "Keenetic" {
			t.Fatalf("X-Device-Model = %q, want Keenetic", got)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     make(http.Header),
		}, nil
	})}
	if _, err := fetchSubscriptionWithClient("https://example.com/sub", client); err != nil {
		t.Fatal(err)
	}
}

func TestFetchSubscriptionReadsHappMetadataFromHeaders(t *testing.T) {
	body := "vless://00000000-0000-0000-0000-000000000002@example.com:443?type=ws&security=tls#supported"
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		header := make(http.Header)
		header.Set("Profile-Title", "base64:RmFzdENvbg==")
		header.Set("Announce", "base64:VXNlIHRoZSBmYXN0ZXN0IHNlcnZlcg==")
		header.Set("Subscription-Userinfo", "upload=10; download=20; total=100; expire=200")
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     header,
		}, nil
	})}

	sub, err := fetchSubscriptionWithClient("https://example.com/sub", client)
	if err != nil {
		t.Fatal(err)
	}
	if sub.Name != "FastCon" || sub.ProviderName != "FastCon" {
		t.Fatalf("names = %q / %q", sub.Name, sub.ProviderName)
	}
	if sub.Description != "Use the fastest server" {
		t.Fatalf("description = %q", sub.Description)
	}
	if sub.UserInfo == nil || sub.UserInfo.Used() != 30 || sub.UserInfo.Total != 100 {
		t.Fatalf("user info = %+v", sub.UserInfo)
	}
}

func TestFetchSubscriptionReadsHappMetadataFromBody(t *testing.T) {
	body := strings.Join([]string{
		"#profile-title: Body VPN",
		"#announce: Line one%0ALine two",
		"#subscription-userinfo: upload=1; download=2; total=10",
		"vless://00000000-0000-0000-0000-000000000002@example.com:443?type=ws&security=tls#supported",
	}, "\n")
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     make(http.Header),
		}, nil
	})}

	sub, err := fetchSubscriptionWithClient("https://example.com/sub", client)
	if err != nil {
		t.Fatal(err)
	}
	if sub.Name != "Body VPN" || sub.Description != "Line one\nLine two" {
		t.Fatalf("metadata = name %q description %q", sub.Name, sub.Description)
	}
	if sub.UserInfo == nil || sub.UserInfo.Used() != 3 {
		t.Fatalf("user info = %+v", sub.UserInfo)
	}
}

func TestPreserveSubscriptionDisplayName(t *testing.T) {
	fresh := &Subscription{Name: "New Provider", ProviderName: "New Provider"}
	previous := &Subscription{
		Name: "example.com", URL: "https://example.com/sub",
	}
	preserveSubscriptionDisplayName(fresh, previous)
	if fresh.Name != "New Provider" {
		t.Fatalf("automatic name = %q", fresh.Name)
	}

	fresh = &Subscription{Name: "New Provider", ProviderName: "New Provider"}
	previous.Name = "My Router"
	preserveSubscriptionDisplayName(fresh, previous)
	if fresh.Name != "My Router" {
		t.Fatalf("manual name = %q", fresh.Name)
	}
}

func TestFetchSubscriptionExcludesUnsupportedTransports(t *testing.T) {
	body := strings.Join([]string{
		"vless://00000000-0000-0000-0000-000000000001@example.com:443?type=xhttp&security=tls#xhttp",
		"vless://00000000-0000-0000-0000-000000000002@example.com:443?type=ws&security=tls#supported",
		"vless://00000000-0000-0000-0000-000000000003@example.com:443?type=mystery&security=tls#unknown",
		"vless://00000000-0000-0000-0000-000000000004@example.com:443?type=raw&security=reality#raw-is-tcp",
	}, "\n")
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     make(http.Header),
		}, nil
	})}
	sub, err := fetchSubscriptionWithClient("https://example.com/sub", client)
	if err != nil {
		t.Fatal(err)
	}
	if len(sub.Servers) != 2 || sub.Servers[0].Network != "ws" || sub.Servers[1].Network != "tcp" {
		t.Fatalf("servers = %#v, want websocket and normalized raw/TCP", sub.Servers)
	}
	if sub.ExcludedServers != 2 {
		t.Fatalf("excluded = %d, want 2", sub.ExcludedServers)
	}
	if sub.ExcludedTransports["xhttp"] != 1 || sub.ExcludedTransports["mystery"] != 1 {
		t.Fatalf("excluded transports = %#v", sub.ExcludedTransports)
	}
}

func TestFetchSubscriptionParsesXrayJSONArray(t *testing.T) {
	body := `[
		{
			"remarks": "Finland",
			"outbounds": [{
				"protocol": "vless",
				"tag": "proxy",
				"settings": {"vnext": [{
					"address": "vpn.example", "port": 443,
					"users": [{"id": "00000000-0000-0000-0000-000000000101", "flow": "xtls-rprx-vision"}]
				}]},
				"streamSettings": {
					"network": "raw", "security": "reality",
					"realitySettings": {
						"serverName": "cdn.example", "fingerprint": "firefox",
						"publicKey": "public-key", "shortId": "abcd"
					}
				}
			}]
		},
		{
			"remarks": "Unsupported",
			"outbounds": [{
				"protocol": "vless",
				"settings": {"vnext": [{
					"address": "xhttp.example", "port": 443,
					"users": [{"id": "00000000-0000-0000-0000-000000000102"}]
				}]},
				"streamSettings": {"network": "xhttp", "security": "tls"}
			}]
		}
	]`
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     make(http.Header),
		}, nil
	})}

	sub, err := fetchSubscriptionWithClient("https://example.com/xray.json", client)
	if err != nil {
		t.Fatal(err)
	}
	if len(sub.Servers) != 1 || sub.ExcludedServers != 1 {
		t.Fatalf("servers=%d excluded=%d", len(sub.Servers), sub.ExcludedServers)
	}
	server := sub.Servers[0]
	if server.Name != "Finland" || server.Address != "vpn.example" ||
		server.Network != "tcp" || server.Security != "reality" ||
		server.SNI != "cdn.example" || server.Fingerprint != "firefox" ||
		server.PublicKey != "public-key" || server.ShortID != "abcd" {
		t.Fatalf("parsed server=%+v", server)
	}
}

func TestPruneUnsupportedServersRemovesXHTTP(t *testing.T) {
	cfg := defaultConfig()
	cfg.Servers = []VLESSServer{
		{ID: "xhttp", Network: "xhttp"},
		{ID: "mystery", Network: "mystery"},
	}
	cfg.ActiveServer = "xhttp"
	if removed := pruneUnsupportedServers(cfg); removed != 2 {
		t.Fatalf("removed = %d, want 2", removed)
	}
	if cfg.ActiveServer != "" {
		t.Fatalf("active server = %q, want empty", cfg.ActiveServer)
	}
	if len(cfg.Servers) != 0 {
		t.Fatalf("servers = %#v", cfg.Servers)
	}
}

func TestLoadSubscriptionsKeepsProfilesWithSharedEndpoint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "subscriptions.json")
	first := VLESSServer{
		ID: "old-shared", Address: "example.com", Port: 443,
		UUID:     "00000000-0000-0000-0000-000000000001",
		Security: "reality", Network: "tcp", SNI: "one.example", ShortID: "1111",
	}
	second := first
	second.SNI = "two.example"
	second.ShortID = "2222"
	input := []*Subscription{{
		ID: "old-sub", URL: "https://example.com/sub",
		Servers: []VLESSServer{first, second}, DisabledServerIDs: []string{"old-shared"},
	}}
	if err := saveSubscriptions(path, input); err != nil {
		t.Fatal(err)
	}

	loaded, err := loadSubscriptions(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 || len(loaded[0].Servers) != 2 {
		t.Fatalf("loaded subscriptions = %#v, want two distinct servers", loaded)
	}
	if loaded[0].Servers[0].ID == loaded[0].Servers[1].ID {
		t.Fatal("migrated profiles still share an ID")
	}
	if len(loaded[0].DisabledServerIDs) != 2 {
		t.Fatalf("disabled IDs = %v, want both migrated profiles disabled", loaded[0].DisabledServerIDs)
	}
}

func TestLoadSubscriptionsMigratesRawTransport(t *testing.T) {
	path := filepath.Join(t.TempDir(), "subscriptions.json")
	input := []*Subscription{{
		ID: "old-sub", URL: "https://example.com/raw",
		Servers: []VLESSServer{{
			ID: "old-raw", Name: "raw", Address: "example.com", Port: 443,
			UUID: "00000000-0000-0000-0000-000000000001", Network: "raw",
		}},
		DisabledServerIDs: []string{"old-raw"},
	}}
	if err := saveSubscriptions(path, input); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadSubscriptions(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 || len(loaded[0].Servers) != 1 {
		t.Fatalf("raw server was removed during migration: %#v", loaded)
	}
	server := loaded[0].Servers[0]
	if server.Network != "tcp" || !isSupportedServer(&server) {
		t.Fatalf("raw server was not normalized: %+v", server)
	}
	if len(loaded[0].DisabledServerIDs) != 1 || loaded[0].DisabledServerIDs[0] != server.ID {
		t.Fatalf("disabled state was not migrated: %#v", loaded[0].DisabledServerIDs)
	}
}

func TestPingResultsMustCoverEveryRequestedServer(t *testing.T) {
	servers := []VLESSServer{{ID: "one"}, {ID: "two"}}
	results := []PingResult{{ServerID: "one", LatencyMS: 10}}
	if _, ok := pingResultsForServers(results, servers); ok {
		t.Fatal("partial ping results must not satisfy a full-server request")
	}
	results = append(results, PingResult{ServerID: "two", LatencyMS: 20})
	if got, ok := pingResultsForServers(results, servers); !ok || len(got) != 2 {
		t.Fatalf("complete results = %#v, ok=%v", got, ok)
	}
}

func TestPreserveSubscriptionOptionsDropsMissingServers(t *testing.T) {
	previous := &Subscription{
		DisabledServerIDs: []string{"keep", "gone"},
		Disabled:          true,
	}
	fresh := &Subscription{Servers: []VLESSServer{{ID: "keep"}, {ID: "auto"}, {ID: "enabled"}}}
	preserveSubscriptionOptions(fresh, previous)
	if len(fresh.DisabledServerIDs) != 1 || fresh.DisabledServerIDs[0] != "keep" {
		t.Fatalf("disabled IDs = %v, want [keep]", fresh.DisabledServerIDs)
	}
	if !fresh.Disabled {
		t.Fatal("subscription disabled state was not preserved")
	}
}

func TestPreserveSubscriptionOptionsMigratesRotatedServerID(t *testing.T) {
	oldServer := VLESSServer{
		ID: "old", Name: "Finland", Address: "vpn.example", Port: 443,
		UUID: "old-uuid", Network: "tcp", Security: "reality",
	}
	newServer := oldServer
	newServer.ID = "new"
	newServer.UUID = "new-uuid"
	previous := &Subscription{
		Servers: []VLESSServer{oldServer}, DisabledServerIDs: []string{oldServer.ID},
	}
	fresh := &Subscription{Servers: []VLESSServer{newServer}}

	preserveSubscriptionOptions(fresh, previous)
	if len(fresh.DisabledServerIDs) != 1 || fresh.DisabledServerIDs[0] != newServer.ID {
		t.Fatalf("disabled IDs = %v, want [%s]", fresh.DisabledServerIDs, newServer.ID)
	}
}

func TestPingCacheMigratesEquivalentRotatedServerID(t *testing.T) {
	cache := newPingCache(filepath.Join(t.TempDir(), "ping-cache.json"))
	oldServer := VLESSServer{
		ID: "old", Name: "Finland", Address: "vpn.example", Port: 443,
		UUID: "old-uuid", Network: "tcp", Security: "reality",
	}
	newServer := oldServer
	newServer.ID = "new"
	newServer.UUID = "new-uuid"
	checkedAt := time.Now().Add(-time.Minute)
	cache.Update([]PingResult{{
		ServerID: oldServer.ID, ServerName: oldServer.Name,
		Address: oldServer.Address, Port: oldServer.Port,
		LatencyMS: -1, Error: "timeout", CheckedAt: checkedAt,
	}})

	if migrated := cache.MigrateEquivalent(
		[]VLESSServer{oldServer}, []VLESSServer{newServer},
	); migrated != 1 {
		t.Fatalf("migrated=%d, want 1", migrated)
	}
	result, ok := cache.Get(newServer.ID)
	if !ok || result.LatencyMS != -1 || result.Error != "timeout" ||
		!result.CheckedAt.Equal(checkedAt) || result.ServerName != newServer.Name {
		t.Fatalf("migrated result=%+v ok=%v", result, ok)
	}
	reloaded := newPingCache(cache.path)
	if _, ok := reloaded.Get(newServer.ID); !ok {
		t.Fatal("migrated result was not persisted")
	}
}

func TestAllServersExcludesDisabledSubscriptionServer(t *testing.T) {
	cfg := defaultConfig()
	cfg.Servers = []VLESSServer{{ID: "disabled"}, {ID: "auto-disabled"}, {ID: "manual", Manual: true}}
	api := &apiServer{
		cfg: cfg,
		subs: []*Subscription{{
			Servers:           []VLESSServer{{ID: "disabled"}, {ID: "former-auto-disabled"}, {ID: "enabled"}},
			DisabledServerIDs: []string{"disabled"},
		}},
	}
	got := api.allServersLocked()
	if len(got) != 3 || got[0].ID != "former-auto-disabled" || got[1].ID != "enabled" || got[2].ID != "manual" {
		t.Fatalf("allServersLocked() = %#v", got)
	}
}

func TestAllServersExcludesDisabledSubscription(t *testing.T) {
	api := &apiServer{
		cfg: defaultConfig(),
		subs: []*Subscription{
			{ID: "off", Disabled: true, Servers: []VLESSServer{{ID: "off-server"}}},
			{ID: "on", Servers: []VLESSServer{{ID: "on-server"}}},
		},
	}
	got := api.allServersLocked()
	if len(got) != 1 || got[0].ID != "on-server" {
		t.Fatalf("allServersLocked() = %#v, want only enabled subscription", got)
	}
}

func TestEnabledServersFromDropsStaleDisabledSnapshot(t *testing.T) {
	api := &apiServer{
		cfg: &Config{Servers: []VLESSServer{
			{ID: "manual", Manual: true},
		}},
		subs: []*Subscription{{
			Servers:           []VLESSServer{{ID: "enabled"}, {ID: "manual-disabled"}, {ID: "former-auto-disabled"}},
			DisabledServerIDs: []string{"manual-disabled"},
		}},
	}

	got := api.enabledServersFrom([]VLESSServer{
		{ID: "enabled"},
		{ID: "manual-disabled"},
		{ID: "former-auto-disabled"},
		{ID: "enabled"},
	})
	if len(got) != 2 || got[0].ID != "enabled" || got[1].ID != "former-auto-disabled" {
		t.Fatalf("enabledServersFrom() = %#v, want every server except manually disabled", got)
	}
}

func TestPruneStaleServersClearsRemovedActiveAndKeepsManual(t *testing.T) {
	cfg := &Config{
		ActiveServer: "removed",
		Servers: []VLESSServer{
			{ID: "removed"},
			{ID: "current"},
			{ID: "manual", Manual: true},
		},
	}
	subs := []*Subscription{{Servers: []VLESSServer{{ID: "current"}}}}

	if pruned := pruneStaleServers(cfg, subs); pruned != 1 {
		t.Fatalf("pruned = %d, want 1", pruned)
	}
	if cfg.ActiveServer != "" {
		t.Fatalf("active server = %q, want empty", cfg.ActiveServer)
	}
	if len(cfg.Servers) != 2 || cfg.Servers[0].ID != "current" || cfg.Servers[1].ID != "manual" {
		t.Fatalf("servers after prune = %#v", cfg.Servers)
	}
}

func TestRefreshPruningPreservesActiveServerMissingFromNewCatalog(t *testing.T) {
	cfg := &Config{
		ActiveServer: "active",
		Servers: []VLESSServer{
			{ID: "active"},
			{ID: "stale"},
			{ID: "current"},
		},
	}
	subs := []*Subscription{{Servers: []VLESSServer{{ID: "current"}}}}

	if pruned := pruneStaleServersPreservingActive(cfg, subs); pruned != 1 {
		t.Fatalf("pruned = %d, want 1", pruned)
	}
	if cfg.ActiveServer != "active" {
		t.Fatalf("active server = %q, want preserved", cfg.ActiveServer)
	}
	if len(cfg.Servers) != 2 || cfg.Servers[0].ID != "active" || cfg.Servers[1].ID != "current" {
		t.Fatalf("servers after refresh pruning = %#v", cfg.Servers)
	}
}

func TestDeleteSubscriptionRemovesCachedActiveServerWhenVPNIsStopped(t *testing.T) {
	tmp := t.TempDir()
	server := VLESSServer{ID: "sub-server", Name: "old"}
	cfg := defaultConfig()
	cfg.ActiveServer = server.ID
	cfg.Servers = []VLESSServer{server, {ID: "manual", Name: "manual", Manual: true}}
	api := newAPIServer(
		NewProcessManager(tmp),
		cfg,
		[]*Subscription{{ID: "old-sub", Servers: []VLESSServer{server}}},
		filepath.Join(tmp, "config.json"),
		filepath.Join(tmp, "subscriptions.json"),
	)
	req := httptest.NewRequest(http.MethodDelete, "/api/subscriptions/old-sub", nil)
	rec := httptest.NewRecorder()
	api.handleSubscriptionByID(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if api.cfg.ActiveServer != "" {
		t.Fatalf("active server = %q, want empty", api.cfg.ActiveServer)
	}
	if len(api.cfg.Servers) != 1 || api.cfg.Servers[0].ID != "manual" {
		t.Fatalf("servers after deletion = %#v", api.cfg.Servers)
	}
}

func TestDeleteSubscriptionKeepsRunningActiveServer(t *testing.T) {
	tmp := t.TempDir()
	server := VLESSServer{ID: "sub-server", Name: "running"}
	cfg := defaultConfig()
	cfg.ActiveServer = server.ID
	cfg.Servers = []VLESSServer{server, {ID: "stale", Name: "stale"}}
	pm := NewProcessManager(tmp)
	pm.running = true
	api := newAPIServer(
		pm,
		cfg,
		[]*Subscription{{ID: "old-sub", Servers: []VLESSServer{server, {ID: "stale"}}}},
		filepath.Join(tmp, "config.json"),
		filepath.Join(tmp, "subscriptions.json"),
	)
	api.pingCache.Set(PingResult{
		ServerID: server.ID, ServerName: server.Name,
		LatencyMS: 123, CheckedAt: time.Now(),
	})

	req := httptest.NewRequest(http.MethodDelete, "/api/subscriptions/old-sub", nil)
	rec := httptest.NewRecorder()
	api.handleSubscriptionByID(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !pm.Status().Running {
		t.Fatal("deleting subscription stopped the running VPN")
	}
	if api.cfg.ActiveServer != server.ID {
		t.Fatalf("active server = %q, want %q", api.cfg.ActiveServer, server.ID)
	}
	if len(api.cfg.Servers) != 1 || api.cfg.Servers[0].ID != server.ID {
		t.Fatalf("servers after deletion = %#v", api.cfg.Servers)
	}
	if len(api.subs) != 0 {
		t.Fatalf("subscription was not deleted: %#v", api.subs)
	}
	if result, ok := api.pingCache.Get(server.ID); !ok || result.LatencyMS != 123 {
		t.Fatalf("active ping result was pruned: %#v, ok=%v", result, ok)
	}
}

func TestStatusKeepsLatencyAndSubscriptionAcrossFingerprintRefresh(t *testing.T) {
	tmp := t.TempDir()
	active := VLESSServer{
		ID: "old-id", Name: "Finland", Address: "147.45.184.53", Port: 443,
		UUID: "old-uuid", Security: "reality",
	}
	refreshed := active
	refreshed.ID = "new-id"
	refreshed.UUID = "new-uuid"
	cfg := defaultConfig()
	cfg.ActiveServer = active.ID
	cfg.Servers = []VLESSServer{active}
	api := newAPIServer(
		NewProcessManager(tmp),
		cfg,
		[]*Subscription{{ID: "sub", Name: "Strelka", Servers: []VLESSServer{refreshed}}},
		filepath.Join(tmp, "config.json"),
		filepath.Join(tmp, "subscriptions.json"),
	)
	api.lastPing = []PingResult{{ServerID: active.ID, ServerName: active.Name, LatencyMS: 350}}

	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	rec := httptest.NewRecorder()
	api.handleStatus(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var status struct {
		Latency int64 `json:"active_server_latency_ms"`
		Details struct {
			Subscription string `json:"subscription"`
		} `json:"active_server_details"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.Latency != 350 || status.Details.Subscription != "Strelka" {
		t.Fatalf("status latency=%d subscription=%q", status.Latency, status.Details.Subscription)
	}
}

func TestStatusFallsBackToActiveTunnelHealthLatency(t *testing.T) {
	tmp := t.TempDir()
	server := VLESSServer{
		ID: "active-id", Name: "Finland", Address: "147.45.184.53", Port: 443,
		Security: "reality",
	}
	cfg := defaultConfig()
	cfg.ActiveServer = server.ID
	cfg.Servers = []VLESSServer{server}
	pm := NewProcessManager(tmp)
	pm.running = true
	pm.startedAt = time.Now()
	api := newAPIServer(
		pm,
		cfg,
		[]*Subscription{{ID: "sub", Name: "Strelka", Servers: []VLESSServer{server}}},
		filepath.Join(tmp, "config.json"),
		filepath.Join(tmp, "subscriptions.json"),
	)
	api.failover.mu.Lock()
	api.failover.state.VPNHealthOK = true
	api.failover.state.VPNHealthCheck = time.Now()
	api.failover.state.VPNHealthLatencyMS = 417
	api.failover.mu.Unlock()

	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	rec := httptest.NewRecorder()
	api.handleStatus(rec, req)
	var status struct {
		Latency int64 `json:"active_server_latency_ms"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.Latency != 417 {
		t.Fatalf("status latency=%d, want active tunnel health latency 417", status.Latency)
	}
}

func TestPrioritySelectionFallsThroughAndStopsAtFirstWorkingSubscription(t *testing.T) {
	tmp := t.TempDir()
	cfg := defaultConfig()
	api := newAPIServer(
		NewProcessManager(tmp),
		cfg,
		[]*Subscription{
			{ID: "first", Name: "first", Servers: []VLESSServer{{ID: "a"}, {ID: "b"}}},
			{ID: "second", Name: "second", Servers: []VLESSServer{{ID: "c"}}},
			{ID: "third", Name: "third", Servers: []VLESSServer{{ID: "d"}}},
		},
		filepath.Join(tmp, "config.json"),
		filepath.Join(tmp, "subscriptions.json"),
	)
	var calls [][]string
	api.pingRunner = func(servers []VLESSServer) []PingResult {
		ids := make([]string, len(servers))
		results := make([]PingResult, len(servers))
		for i, server := range servers {
			ids[i] = server.ID
			results[i] = PingResult{ServerID: server.ID, ServerName: server.ID, LatencyMS: -1}
			if server.ID == "c" {
				results[i].LatencyMS = 25
			}
		}
		calls = append(calls, ids)
		return results
	}

	server, result := api.findBestPrioritized("", false)
	if server == nil || result == nil || server.ID != "c" {
		t.Fatalf("selected server = %#v, result = %#v", server, result)
	}
	if len(calls) != 2 || strings.Join(calls[0], ",") != "a,b" || strings.Join(calls[1], ",") != "c" {
		t.Fatalf("priority ping calls = %#v", calls)
	}
}

func TestConfiguredSelectionCanChooseGloballyFastestServer(t *testing.T) {
	tmp := t.TempDir()
	cfg := defaultConfig()
	cfg.Settings.PingSelectionMode = "fastest"
	api := newAPIServer(
		NewProcessManager(tmp),
		cfg,
		[]*Subscription{
			{ID: "first", Servers: []VLESSServer{{ID: "a", Name: "priority"}}},
			{ID: "second", Servers: []VLESSServer{{ID: "b", Name: "fast"}}},
		},
		filepath.Join(tmp, "config.json"),
		filepath.Join(tmp, "subscriptions.json"),
	)
	api.pingRunner = func(servers []VLESSServer) []PingResult {
		return []PingResult{
			{ServerID: "a", ServerName: "priority", LatencyMS: 100},
			{ServerID: "b", ServerName: "fast", LatencyMS: 10},
		}
	}

	server, result := api.findBestConfigured("", false, false)
	if server == nil || result == nil || server.ID != "b" {
		t.Fatalf("selected server = %#v, result = %#v", server, result)
	}
}

func TestPrioritizedSelectionChoosesFastestServerInFirstWorkingSubscription(t *testing.T) {
	tmp := t.TempDir()
	cfg := defaultConfig()
	cfg.Settings.PingSelectionMode = "priority"
	api := newAPIServer(
		NewProcessManager(tmp),
		cfg,
		[]*Subscription{
			{ID: "first", Servers: []VLESSServer{{ID: "a"}, {ID: "b"}}},
			{ID: "second", Servers: []VLESSServer{{ID: "c"}}},
		},
		filepath.Join(tmp, "config.json"),
		filepath.Join(tmp, "subscriptions.json"),
	)
	var calls [][]string
	api.pingRunner = func(servers []VLESSServer) []PingResult {
		ids := make([]string, len(servers))
		for i, server := range servers {
			ids[i] = server.ID
		}
		calls = append(calls, ids)
		return []PingResult{
			{ServerID: "a", ServerName: "a", LatencyMS: 100},
			{ServerID: "b", ServerName: "b", LatencyMS: 20},
		}
	}

	server, result := api.findBestConfigured("", false, false)
	if server == nil || result == nil || server.ID != "b" || result.LatencyMS != 20 {
		t.Fatalf("selected server = %#v, result = %#v", server, result)
	}
	if len(calls) != 1 || strings.Join(calls[0], ",") != "a,b" {
		t.Fatalf("priority ping calls = %#v, want only first subscription", calls)
	}
}

func TestCancelledPrioritizedSelectionDoesNotContinueToNextSubscription(t *testing.T) {
	tmp := t.TempDir()
	cfg := defaultConfig()
	cfg.Settings.PingSelectionMode = "priority"
	api := newAPIServer(
		NewProcessManager(tmp),
		cfg,
		[]*Subscription{
			{ID: "first", Servers: []VLESSServer{{ID: "a"}}},
			{ID: "second", Servers: []VLESSServer{{ID: "b"}}},
		},
		filepath.Join(tmp, "config.json"),
		filepath.Join(tmp, "subscriptions.json"),
	)
	calls := 0
	api.pingRunner = func([]VLESSServer) []PingResult {
		calls++
		api.cancelPingRun("test")
		return nil
	}

	server, result := api.findBestConfigured("", false, false)
	if server != nil || result != nil {
		t.Fatalf("cancelled selection returned server=%#v result=%#v", server, result)
	}
	if calls != 1 {
		t.Fatalf("cancelled selection made %d subscription calls, want 1", calls)
	}
}

func TestConfiguredSelectionUsesOnlyCompleteFreshCache(t *testing.T) {
	tmp := t.TempDir()
	cfg := defaultConfig()
	cfg.Settings.PingUseFreshCache = true
	cfg.Settings.PingCacheMaxAgeMin = 15
	api := newAPIServer(
		NewProcessManager(tmp),
		cfg,
		[]*Subscription{{ID: "first", Servers: []VLESSServer{{ID: "a"}, {ID: "b"}}}},
		filepath.Join(tmp, "config.json"),
		filepath.Join(tmp, "subscriptions.json"),
	)
	api.pingCache.Update([]PingResult{
		{ServerID: "a", ServerName: "a", LatencyMS: 30, CheckedAt: time.Now()},
		{ServerID: "b", ServerName: "b", LatencyMS: 10, CheckedAt: time.Now()},
	})
	pingCalls := 0
	api.pingRunner = func(servers []VLESSServer) []PingResult {
		pingCalls++
		return nil
	}

	server, _ := api.findBestConfigured("", false, true)
	if server == nil || server.ID != "b" || pingCalls != 0 {
		t.Fatalf("fresh cache selected %#v with %d ping calls", server, pingCalls)
	}

	api.pingCache.Set(PingResult{
		ServerID: "a", ServerName: "a", LatencyMS: 30,
		CheckedAt: time.Now().Add(-time.Hour),
	})
	api.pingRunner = func(servers []VLESSServer) []PingResult {
		pingCalls++
		return []PingResult{
			{ServerID: "a", ServerName: "a", LatencyMS: 20},
			{ServerID: "b", ServerName: "b", LatencyMS: 40},
		}
	}
	server, _ = api.findBestConfigured("", false, true)
	if server == nil || server.ID != "b" || pingCalls != 0 {
		t.Fatalf("partial fresh cache selected %#v with %d ping calls", server, pingCalls)
	}
}

func TestPrepareServerForStartRunsFreshCompletePing(t *testing.T) {
	tmp := t.TempDir()
	cfg := defaultConfig()
	cfg.Settings.PingSelectionMode = "priority"
	api := newAPIServer(
		NewProcessManager(tmp),
		cfg,
		[]*Subscription{{ID: "first", Servers: []VLESSServer{
			{ID: "slow", Name: "slow"},
			{ID: "fast", Name: "fast"},
		}}},
		filepath.Join(tmp, "config.json"),
		filepath.Join(tmp, "subscriptions.json"),
	)
	api.pingCache.Update([]PingResult{
		{ServerID: "slow", ServerName: "slow", LatencyMS: 90, CheckedAt: time.Now()},
		{ServerID: "fast", ServerName: "fast", LatencyMS: 15, CheckedAt: time.Now()},
	})
	pingCalls := 0
	api.pingRunner = func([]VLESSServer) []PingResult {
		pingCalls++
		return []PingResult{
			{ServerID: "slow", ServerName: "slow", LatencyMS: 90},
			{ServerID: "fast", ServerName: "fast", LatencyMS: 15},
		}
	}

	name, err := api.prepareServerForStart()
	if err != nil {
		t.Fatal(err)
	}
	if name != "fast" || cfg.ActiveServer != "fast" {
		t.Fatalf("selected name=%q active=%q", name, cfg.ActiveServer)
	}
	if pingCalls != 1 {
		t.Fatalf("fresh start made %d ping calls, want 1 complete pass", pingCalls)
	}
}

func TestPriorityFailoverRotatesFromActiveSubscription(t *testing.T) {
	tmp := t.TempDir()
	api := newAPIServer(
		NewProcessManager(tmp),
		defaultConfig(),
		[]*Subscription{
			{ID: "first", Servers: []VLESSServer{{ID: "a"}}},
			{ID: "second", Servers: []VLESSServer{{ID: "active"}, {ID: "b"}}},
			{ID: "third", Servers: []VLESSServer{{ID: "c"}}},
		},
		filepath.Join(tmp, "config.json"),
		filepath.Join(tmp, "subscriptions.json"),
	)
	var calls []string
	api.pingRunner = func(servers []VLESSServer) []PingResult {
		calls = append(calls, servers[0].ID)
		latency := int64(-1)
		if servers[0].ID == "a" {
			latency = 30
		}
		return []PingResult{{ServerID: servers[0].ID, ServerName: servers[0].ID, LatencyMS: latency}}
	}

	server, _ := api.findBestPrioritized("active", true)
	if server == nil || server.ID != "a" {
		t.Fatalf("selected server = %#v, want wrapped first subscription", server)
	}
	if strings.Join(calls, ",") != "b,c,a" {
		t.Fatalf("cyclic calls = %v, want b,c,a", calls)
	}
}

func TestMoveAndDisableSubscription(t *testing.T) {
	tmp := t.TempDir()
	active := VLESSServer{ID: "active"}
	cfg := defaultConfig()
	cfg.ActiveServer = active.ID
	cfg.Servers = []VLESSServer{active}
	api := newAPIServer(
		NewProcessManager(tmp),
		cfg,
		[]*Subscription{
			{ID: "first", Name: "first", Servers: []VLESSServer{{ID: "a"}}},
			{ID: "second", Name: "second", Servers: []VLESSServer{active}},
		},
		filepath.Join(tmp, "config.json"),
		filepath.Join(tmp, "subscriptions.json"),
	)

	moveReq := httptest.NewRequest(http.MethodPost, "/api/subscriptions/second/move", bytes.NewBufferString(`{"direction":-1}`))
	moveRec := httptest.NewRecorder()
	api.handleSubscriptionByID(moveRec, moveReq)
	if moveRec.Code != http.StatusOK || api.subs[0].ID != "second" {
		t.Fatalf("move status = %d, order = %s,%s", moveRec.Code, api.subs[0].ID, api.subs[1].ID)
	}

	disableReq := httptest.NewRequest(http.MethodPatch, "/api/subscriptions/second", bytes.NewBufferString(`{"disabled":true}`))
	disableRec := httptest.NewRecorder()
	api.handleSubscriptionByID(disableRec, disableReq)
	if disableRec.Code != http.StatusOK {
		t.Fatalf("disable status = %d, body = %s", disableRec.Code, disableRec.Body.String())
	}
	if !api.subs[0].Disabled || api.cfg.ActiveServer != "" {
		t.Fatalf("disabled = %v, active = %q", api.subs[0].Disabled, api.cfg.ActiveServer)
	}
}

func TestAutoSelectSingleSubscriptionServerCachesProfile(t *testing.T) {
	tmp := t.TempDir()
	server := VLESSServer{ID: "only-server", Name: "only", Address: "example.com", UUID: "uuid"}
	cfg := defaultConfig()
	api := newAPIServer(
		NewProcessManager(tmp),
		cfg,
		[]*Subscription{{ID: "sub", Servers: []VLESSServer{server}}},
		filepath.Join(tmp, "config.json"),
		filepath.Join(tmp, "subscriptions.json"),
	)
	api.pingRunner = func(servers []VLESSServer) []PingResult {
		return []PingResult{{ServerID: servers[0].ID, ServerName: servers[0].Name, LatencyMS: 10}}
	}

	if selected := api.autoSelectBest(); selected != server.Name {
		t.Fatalf("selected = %q, want %q", selected, server.Name)
	}
	if cfg.ActiveServer != server.ID {
		t.Fatalf("active server = %q, want %q", cfg.ActiveServer, server.ID)
	}
	if len(cfg.Servers) != 1 || cfg.Servers[0].ID != server.ID {
		t.Fatalf("cached servers = %#v", cfg.Servers)
	}
}

func TestEditSubscriptionURLChangesStableID(t *testing.T) {
	oldURL := "https://old.example/sub"
	sub := &Subscription{ID: subscriptionID(oldURL), Name: "test", URL: oldURL}
	cfg := defaultConfig()
	tmp := t.TempDir()
	api := newAPIServer(
		NewProcessManager(tmp),
		cfg,
		[]*Subscription{sub},
		filepath.Join(tmp, "config.json"),
		filepath.Join(tmp, "subscriptions.json"),
	)
	body := bytes.NewBufferString(`{"url":"https://new.example/sub"}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/subscriptions/"+sub.ID, body)
	rec := httptest.NewRecorder()
	api.handleSubscriptionByID(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if api.subs[0].URL != "https://new.example/sub" {
		t.Fatalf("URL = %q", api.subs[0].URL)
	}
	if api.subs[0].ID != subscriptionID(api.subs[0].URL) {
		t.Fatal("ID was not updated with URL")
	}
}

func TestDisableActiveSubscriptionServerClearsSelection(t *testing.T) {
	server := VLESSServer{ID: "server-1", Name: "one"}
	sub := &Subscription{
		ID:      "sub-1",
		Name:    "test",
		URL:     "https://example.com/sub",
		Servers: []VLESSServer{server},
	}
	cfg := defaultConfig()
	cfg.ActiveServer = server.ID
	cfg.Servers = []VLESSServer{server}
	tmp := t.TempDir()
	api := newAPIServer(
		NewProcessManager(tmp),
		cfg,
		[]*Subscription{sub},
		filepath.Join(tmp, "config.json"),
		filepath.Join(tmp, "subscriptions.json"),
	)
	body := bytes.NewBufferString(`{"disabled":true}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/subscriptions/sub-1/servers/server-1", body)
	rec := httptest.NewRecorder()
	api.handleSubscriptionByID(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if api.cfg.ActiveServer != "" {
		t.Fatalf("active server = %q, want empty", api.cfg.ActiveServer)
	}
	if !api.subs[0].serverDisabled(server.ID) {
		t.Fatal("server was not persisted as disabled")
	}
}
