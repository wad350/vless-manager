package main

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func newBehaviorTestAPI(t *testing.T, cfg *Config, subs []*Subscription) *apiServer {
	t.Helper()
	dir := t.TempDir()
	return newAPIServer(
		NewProcessManager(dir),
		cfg,
		subs,
		filepath.Join(dir, "config.json"),
		filepath.Join(dir, "subscriptions.json"),
	)
}

func TestPreferGroupMemberMovesWinnerFirst(t *testing.T) {
	server := VLESSServer{Name: "auto", Members: []VLESSServer{
		{ID: "slow", Address: "slow.example", Port: 443},
		{ID: "fast", Address: "fast.example", Port: 8443},
		{ID: "other", Address: "other.example", Port: 443},
	}}
	got := preferGroupMember(server, "fast")
	if got.Members[0].ID != "fast" || got.Address != "fast.example" || got.Port != 8443 {
		t.Fatalf("winner was not promoted: %+v", got)
	}
	if server.Members[0].ID != "slow" {
		t.Fatal("source profile was mutated")
	}
}

func TestRunPingAllHandlesIncompatibleServer(t *testing.T) {
	cfg := defaultConfig()
	server := VLESSServer{ID: "unsupported", Name: "unsupported", Address: "127.0.0.1", Port: 443, Network: "mystery"}
	api := newBehaviorTestAPI(t, cfg, []*Subscription{{ID: "sub", Servers: []VLESSServer{server}}})

	results := api.runPingAll([]VLESSServer{server})
	if len(results) != 1 || !results[0].Incompat || results[0].LatencyMS != -1 {
		t.Fatalf("results=%+v", results)
	}
	if cached, ok := api.pingCache.Get(server.ID); !ok || !cached.Incompat {
		t.Fatalf("cached=%+v ok=%v", cached, ok)
	}
	if api.pingProgress.Running {
		t.Fatalf("progress=%+v", api.pingProgress)
	}
}

func TestEffectivePingParallelProtectsLowMemoryRouter(t *testing.T) {
	tests := []struct {
		requested int
		memoryKB  int64
		want      int
		reason    string
	}{
		{0, 124 * 1024, 1, ""},
		{1, 124 * 1024, 1, ""},
		{2, 124 * 1024, 1, "memory_below_256mb"},
		{2, 512 * 1024, 2, ""},
		{2, 0, 2, ""},
	}
	for _, test := range tests {
		got, reason := effectivePingParallel(test.requested, test.memoryKB)
		if got != test.want || reason != test.reason {
			t.Fatalf("requested=%d memory=%d got=%d/%q want=%d/%q",
				test.requested, test.memoryKB, got, reason, test.want, test.reason)
		}
	}
}

func TestConfiguredAlternativeSelectionOrders(t *testing.T) {
	cfg := defaultConfig()
	cfg.ActiveServer = "active"
	subs := []*Subscription{
		{ID: "first", Servers: []VLESSServer{{ID: "first", Name: "first"}}},
		{ID: "second", Servers: []VLESSServer{{ID: "active", Name: "active"}, {ID: "same", Name: "same"}}},
	}
	api := newBehaviorTestAPI(t, cfg, subs)
	api.pingRunner = func(servers []VLESSServer) []PingResult {
		results := make([]PingResult, len(servers))
		for i, server := range servers {
			results[i] = PingResult{ServerID: server.ID, ServerName: server.Name, LatencyMS: 10}
		}
		return results
	}

	cfg.Settings.PingFailoverOrder = "active_first"
	if got := api.chooseAlternativeServer(); got != "same" {
		t.Fatalf("active-first selected %q", got)
	}
	cfg.ActiveServer = "active"
	cfg.Settings.PingFailoverOrder = "priority"
	if got := api.chooseAlternativeServer(); got != "first" {
		t.Fatalf("priority selected %q", got)
	}

	api.pingRunner = func([]VLESSServer) []PingResult { return nil }
	if got := api.chooseAlternativeServer(); got != "" {
		t.Fatalf("failed selection=%q", got)
	}
}

func TestFailoverNeverSelectsFromPingCache(t *testing.T) {
	cfg := defaultConfig()
	cfg.ActiveServer = "active"
	active := VLESSServer{ID: "active", Name: "active", Address: "active.example", Port: 443}
	replacement := VLESSServer{ID: "replacement", Name: "replacement", Address: "replacement.example", Port: 443}
	api := newBehaviorTestAPI(t, cfg, []*Subscription{
		{ID: "priority", Servers: []VLESSServer{active, replacement}},
	})
	api.pingCache.Set(PingResult{
		ServerID: replacement.ID, ServerName: replacement.Name,
		LatencyMS: 10, CheckedAt: time.Now(),
	})
	pingCalls := 0
	api.pingRunner = func(servers []VLESSServer) []PingResult {
		pingCalls++
		return []PingResult{{
			ServerID: replacement.ID, ServerName: replacement.Name,
			LatencyMS: -1, CheckedAt: time.Now(),
		}}
	}

	if got := api.chooseAlternativeServer(); got != "" {
		t.Fatalf("failover selected cached but currently unavailable server %q", got)
	}
	if pingCalls != 1 {
		t.Fatalf("fresh failover probes=%d, want 1", pingCalls)
	}
}

func TestPrepareServerAlwaysRequiresSuccessfulPing(t *testing.T) {
	cfg := defaultConfig()
	active := VLESSServer{ID: "active", Name: "active"}
	cfg.ActiveServer = active.ID
	api := newBehaviorTestAPI(t, cfg, []*Subscription{{ID: "sub", Servers: []VLESSServer{active}}})
	api.pingRunner = func([]VLESSServer) []PingResult {
		return []PingResult{{ServerID: active.ID, ServerName: active.Name, LatencyMS: 25}}
	}
	if got, err := api.prepareServerForStart(); err != nil || got != active.Name {
		t.Fatalf("verified active got=%q err=%v", got, err)
	}

	api.pingRunner = func([]VLESSServer) []PingResult { return nil }
	if _, err := api.prepareServerForStart(); err == nil || cfg.ActiveServer != "" {
		t.Fatalf("unverified server must fail and clear active server, active=%q err=%v", cfg.ActiveServer, err)
	}
	if err := api.startVPNInternal(); err == nil {
		t.Fatal("expected internal start error without a verified server")
	}
}

func TestPrepareServerClearsStaleSelectionWhilePinging(t *testing.T) {
	cfg := defaultConfig()
	server := VLESSServer{ID: "fresh", Name: "fresh"}
	cfg.ActiveServer = "stale-from-other-subscription"
	api := newBehaviorTestAPI(t, cfg, []*Subscription{{ID: "priority", Servers: []VLESSServer{server}}})

	started := make(chan struct{})
	release := make(chan struct{})
	api.pingRunner = func([]VLESSServer) []PingResult {
		close(started)
		<-release
		return []PingResult{{ServerID: server.ID, ServerName: server.Name, LatencyMS: 20}}
	}

	done := make(chan error, 1)
	go func() {
		_, err := api.prepareServerForStart()
		done <- err
	}()
	<-started
	if cfg.ActiveServer != "" {
		t.Fatalf("stale active server remained visible during selection: %q", cfg.ActiveServer)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if cfg.ActiveServer != server.ID {
		t.Fatalf("selected server = %q, want %q", cfg.ActiveServer, server.ID)
	}
}

func TestInternetCheckUsesConfiguredProbesAndLogsFailures(t *testing.T) {
	oldProbe := wanProbeCall
	defer func() { wanProbeCall = oldProbe }()
	cfg := defaultConfig()
	cfg.Settings.OpenProbes = []string{"http://ok", "http://bad"}
	api := newBehaviorTestAPI(t, cfg, nil)
	wanProbeCall = func(url string, _ time.Duration) (bool, error) {
		if url == "http://ok" {
			return true, nil
		}
		return false, errors.New("offline")
	}
	status := api.checkInternet("test")
	if !status.OK || status.Reachable != 1 || status.Total != 2 {
		t.Fatalf("status=%+v", status)
	}
	wanProbeCall = func(string, time.Duration) (bool, error) {
		return false, errors.New("offline")
	}
	status = api.checkInternet("test")
	if status.OK || status.Reachable != 0 {
		t.Fatalf("failed status=%+v", status)
	}
}
