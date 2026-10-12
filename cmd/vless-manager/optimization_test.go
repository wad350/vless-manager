package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestUnchangedJSONDoesNotRewriteFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	value := map[string]int{"port": 3001}
	if err := writeJSONAtomic(path, value); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(path)
	if err := writeJSONAtomic(path, value); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(path)
	if !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("unchanged content caused a flash rewrite")
	}
}

func TestSubscriptionDiagnosticsDoNotRewriteCatalog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "subscriptions.json")
	subs := []*Subscription{{ID: "sub", URL: "https://example.com/sub", UpdatedAt: time.Now(),
		UserInfo: &SubUserInfo{Download: 10, Total: 100, Expire: 123}}}
	if err := saveSubscriptions(path, subs); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(path)
	subs[0].UpdatedAt = time.Now().Add(time.Hour)
	subs[0].Error = "temporary network failure"
	subs[0].UserInfo.Download = 20
	if err := saveSubscriptions(path, subs); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(path)
	if !os.SameFile(before, after) {
		t.Fatal("volatile subscription fields rewrote the catalog")
	}
	for _, change := range []func(){
		func() { subs[0].URL = "https://example.com/new" },
		func() { subs[0].Disabled = true },
		func() { subs[0].UserInfo.Expire++ },
		func() { subs[0].UserInfo.Total++ },
		func() { subs[0].Servers = []VLESSServer{{ID: "new", UUID: "rotated"}} },
	} {
		before, _ = os.Stat(path)
		change()
		if err := saveSubscriptions(path, subs); err != nil {
			t.Fatal(err)
		}
		after, _ = os.Stat(path)
		if os.SameFile(before, after) {
			t.Fatal("essential catalog change was not persisted")
		}
	}
}

func TestRuntimeCachesNeverUpdateLegacyFiles(t *testing.T) {
	dataDir, ramDir := t.TempDir(), t.TempDir()
	legacyPing := filepath.Join(dataDir, "ping-cache.json")
	old := PingResult{ServerID: "old", LatencyMS: 50}
	if err := writeJSONAtomic(legacyPing, map[string]PingResult{"old": old}); err != nil {
		t.Fatal(err)
	}
	checked := time.Now()
	legacyUpdate := filepath.Join(dataDir, "update-status.json")
	if err := writeJSONAtomic(legacyUpdate, UpdateStatus{LatestVersion: "99.0.0", CheckedAt: &checked}); err != nil {
		t.Fatal(err)
	}
	beforePing, _ := os.ReadFile(legacyPing)
	beforeUpdate, _ := os.ReadFile(legacyUpdate)
	cfg := &Config{Settings: defaultSettings()}
	api := newAPIServerWithRuntimeDir(NewProcessManager(dataDir), cfg, nil,
		filepath.Join(dataDir, "config.json"), filepath.Join(dataDir, "subscriptions.json"), ramDir)
	if _, ok := api.pingCache.Get("old"); !ok {
		t.Fatal("legacy ping history was lost")
	}
	if api.updater.snapshot().LatestVersion != "99.0.0" {
		t.Fatal("legacy release cache was lost")
	}
	api.pingCache.Update([]PingResult{{ServerID: "new", LatencyMS: 25}})
	api.updater.persist(UpdateStatus{LatestVersion: "100.0.0", CheckedAt: &checked})
	afterPing, _ := os.ReadFile(legacyPing)
	afterUpdate, _ := os.ReadFile(legacyUpdate)
	if !bytes.Equal(beforePing, afterPing) || !bytes.Equal(beforeUpdate, afterUpdate) {
		t.Fatal("volatile cache modified persistent state")
	}
	if _, ok := newPingCache(filepath.Join(ramDir, "ping-cache.json")).Get("new"); !ok {
		t.Fatal("RAM cache did not survive a process restart")
	}
}

func TestRingWrapAndClear(t *testing.T) {
	logs := newRingBuffer()
	for i := 1; i <= logBufSize*5; i++ {
		logs.logEvent(serviceLogError, "xray", "runtime", strconv.Itoa(i))
	}
	entries, seq := logs.Entries(0)
	if seq != logBufSize*5 || len(entries) != logBufSize || entries[0].Seq != seq-logBufSize+1 {
		t.Fatalf("bad ring snapshot: seq=%d entries=%d", seq, len(entries))
	}
	for i, entry := range entries {
		if entry.Seq != seq-logBufSize+1+i || entry.Message != strconv.Itoa(entry.Seq) {
			t.Fatalf("entry out of order: %+v", entry)
		}
	}
	lines, gotSeq := logs.Lines(seq - 2)
	if gotSeq != seq || len(lines) != 2 {
		t.Fatalf("incremental lines: seq=%d len=%d", gotSeq, len(lines))
	}
	logs.clear()
	for _, entry := range logs.entries {
		if entry.Message != "" || entry.Fields != nil {
			t.Fatal("clear retained old log allocations")
		}
	}
}

func TestStructuredLogsSkipLegacyRendering(t *testing.T) {
	api := newHandlerTestAPI(t)
	api.pm.event(serviceLogError, "xray", "runtime", "connection refused")
	var response struct {
		Lines   []string          `json:"lines"`
		Entries []serviceLogEntry `json:"entries"`
		Seq     int               `json:"seq"`
	}
	for _, query := range []string{"", "&format=structured"} {
		w := apiRequest(t, api, http.MethodGet, "/api/logs?since=0"+query, "")
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if len(response.Entries) != 1 || response.Entries[0].Seq != response.Seq {
			t.Fatal("log snapshot and sequence disagree")
		}
		if query == "" && len(response.Lines) != 1 {
			t.Fatal("legacy logs API lost its formatted lines")
		}
		if query != "" && len(response.Lines) != 0 {
			t.Fatal("structured logs rendered duplicate legacy lines")
		}
	}
}

func BenchmarkLogsAPI(b *testing.B) {
	for _, format := range []string{"legacy", "structured"} {
		b.Run(format, func(b *testing.B) {
			dir := b.TempDir()
			cfg := &Config{Settings: defaultSettings()}
			cfg.Settings.AuthEnabled = false
			api := newAPIServer(NewProcessManager(dir), cfg, nil, filepath.Join(dir, "config.json"), filepath.Join(dir, "subscriptions.json"))
			for i := 0; i < logBufSize; i++ {
				api.pm.logs.logEvent(serviceLogError, "xray", "runtime", "connection refused", field("xray_level", "error"))
			}
			r := httptest.NewRequest("GET", "/api/logs?since=0&format="+format, nil)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				api.ServeHTTP(httptest.NewRecorder(), r)
			}
		})
	}
}

func diagnosticAPI(t *testing.T) (*apiServer, string) {
	t.Helper()
	dir := t.TempDir()
	cfg := &Config{Settings: defaultSettings()}
	api := newAPIServer(NewProcessManager(dir), cfg, nil,
		filepath.Join(dir, "config.json"), filepath.Join(dir, "subscriptions.json"))
	api.diagnosticsEnabled = true
	token, _, err := api.auth.createSession("admin", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return api, token
}

func TestDiagnosticsAccessAndBounds(t *testing.T) {
	for _, tt := range []struct {
		name, path, remote, method     string
		enabled, authenticated, uiAuth bool
		want                           int
	}{
		{"disabled", "/runtime", "192.168.201.62:1234", "GET", false, true, true, 404},
		{"goroutines disabled", "/goroutines", "192.168.201.62:1234", "GET", false, true, true, 404},
		{"goroutines authenticated", "/goroutines", "192.168.201.62:1234", "GET", true, false, false, 401},
		{"WAN forbidden", "/runtime", "8.8.8.8:1234", "GET", true, true, true, 403},
		{"session required with open UI", "/runtime", "192.168.201.62:1234", "GET", true, false, false, 401},
		{"method", "/runtime", "192.168.201.62:1234", "POST", true, true, true, 405},
		{"duration", "/cpu?seconds=16", "192.168.201.62:1234", "GET", true, true, true, 400},
		{"duration invalid", "/cpu?seconds=no", "192.168.201.62:1234", "GET", true, true, true, 400},
		{"mapped LAN", "/runtime", "[::ffff:192.168.201.62]:1234", "GET", true, true, true, 200},
	} {
		t.Run(tt.name, func(t *testing.T) {
			api, token := diagnosticAPI(t)
			api.diagnosticsEnabled = tt.enabled
			api.cfg.Settings.AuthEnabled = tt.uiAuth
			r := httptest.NewRequest(tt.method, "/api/diagnostics"+tt.path, nil)
			r.RemoteAddr = tt.remote
			if tt.authenticated {
				r.AddCookie(&http.Cookie{Name: authCookieName, Value: token})
			}
			w := httptest.NewRecorder()
			api.ServeHTTP(w, r)
			if w.Code != tt.want {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
		})
	}
}

func TestProfilesStreamAndReleaseGuard(t *testing.T) {
	api, token := diagnosticAPI(t)
	for _, path := range []string{"heap", "cpu?seconds=1"} {
		r := httptest.NewRequest("GET", "/api/diagnostics/"+path, nil)
		r.RemoteAddr = "192.168.201.62:1234"
		r.AddCookie(&http.Cookie{Name: authCookieName, Value: token})
		if path != "heap" {
			ctx, cancel := context.WithCancel(r.Context())
			cancel()
			r = r.WithContext(ctx)
		}
		api.profileBusy.Store(true)
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != 409 {
			t.Fatalf("concurrent profile was not blocked: %d", w.Code)
		}
		api.profileBusy.Store(false)
		w = httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != 200 || w.Body.Len() < 2 || !bytes.Equal(w.Body.Bytes()[:2], []byte{0x1f, 0x8b}) {
			t.Fatalf("invalid profile: status=%d len=%d", w.Code, w.Body.Len())
		}
		if api.profileBusy.Load() {
			t.Fatal("profile guard not released")
		}
	}
}

func BenchmarkLogAppend(b *testing.B) {
	logs := newRingBuffer()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		logs.logEventUnfiltered(serviceLogError, "xray", "runtime", "connection refused")
	}
}

func BenchmarkLegacyLogAppend(b *testing.B) {
	entries := make([]serviceLogEntry, 0, logBufSize)
	lines := make([]string, 0, logBufSize)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if len(entries) >= logBufSize {
			entries, lines = entries[1:], lines[1:]
		}
		entry := serviceLogEntry{Seq: i + 1, Timestamp: time.Now(), Level: "ERROR", Component: "xray", Event: "runtime", Message: "connection refused"}
		entries = append(entries, entry)
		lines = append(lines, formatServiceLogEntry(entry))
	}
}
