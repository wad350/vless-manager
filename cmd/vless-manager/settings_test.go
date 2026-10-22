package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultSettingsAreValid(t *testing.T) {
	if err := defaultSettings().validate(); err != nil {
		t.Fatalf("default settings must be valid: %v", err)
	}
}

func TestLoadConfigKeepsDefaultsForMissingSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"port":3001,"settings":{"log_level":"error","service_log_level":"info"}}`), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Settings.PingSelectionMode != "priority" {
		t.Fatalf("missing ping_selection_mode = %q, want priority default", cfg.Settings.PingSelectionMode)
	}
}

func TestLoadConfigIgnoresRetiredInternetCheckSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	data := `{"port":3001,"settings":{"internet_check_interval_sec":3600,"internet_check_timeout_sec":5,"failover_outer_interval_sec":30,"failover_probe_timeout_sec":7}}`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Settings.FailoverOuterIntervalSec != 30 || cfg.Settings.FailoverProbeTimeoutSec != 7 {
		t.Fatalf("operator settings changed: %+v", cfg.Settings)
	}
	encoded, err := json.Marshal(cfg.Settings)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "internet_check_") {
		t.Fatalf("retired settings still published: %s", encoded)
	}
}

func TestSettingsValidationRejectsUnsafeParallelPing(t *testing.T) {
	settings := defaultSettings()
	settings.PingMaxParallel = -1
	if err := settings.validate(); err == nil || !strings.Contains(err.Error(), "ping_max_parallel") {
		t.Fatalf("expected ping_max_parallel error, got %v", err)
	}
}

func TestSettingsValidationRejectsInvalidProbeURL(t *testing.T) {
	settings := defaultSettings()
	settings.OpenProbes = []string{"not a URL"}
	if err := settings.validate(); err == nil || !strings.Contains(err.Error(), "open_probes") {
		t.Fatalf("expected open_probes error, got %v", err)
	}
}

func TestSettingsValidationRejectsInvalidSharedVPNTestURL(t *testing.T) {
	settings := defaultSettings()
	settings.PingTestURL = "not a URL"
	if err := settings.validate(); err == nil || !strings.Contains(err.Error(), "ping_test_url") {
		t.Fatalf("expected ping_test_url error, got %v", err)
	}
}

func TestLegacyVPNHealthURLMigratesToSharedTestURL(t *testing.T) {
	tests := []struct {
		name, data, want string
	}{
		{"defaults", `{}`, defaultPingTestURL},
		{"old defaults", `{"ping_test_url":"http://www.gstatic.com/generate_204","failover_health_url":"http://cp.cloudflare.com/generate_204"}`, defaultPingTestURL},
		{"legacy custom", `{"failover_health_url":"https://health.example/check"}`, "https://health.example/check"},
		{"legacy custom with default test", `{"ping_test_url":"http://www.gstatic.com/generate_204","failover_health_url":"https://health.example/check"}`, "https://health.example/check"},
		{"custom test wins conflict", `{"ping_test_url":"https://test.example/check","failover_health_url":"https://health.example/check"}`, "https://test.example/check"},
		{"unrelated patch keeps target", `{"ping_max_parallel":2}`, "https://existing.example/check"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			settings := defaultSettings()
			if test.name == "unrelated patch keeps target" {
				settings.PingTestURL = test.want
			}
			if err := json.Unmarshal([]byte(test.data), &settings); err != nil {
				t.Fatal(err)
			}
			if settings.PingTestURL != test.want {
				t.Fatalf("shared URL=%q, want %q", settings.PingTestURL, test.want)
			}
			if err := settings.validate(); err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(settings)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), "failover_health_url") {
				t.Fatal("legacy duplicate URL was serialized")
			}
		})
	}
}

func TestLoadConfigMigratesLegacyVPNHealthURL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	data := `{"settings":{"ping_test_url":"http://www.gstatic.com/generate_204","failover_health_url":"https://health.example/check"}}`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Settings.PingTestURL != "https://health.example/check" {
		t.Fatalf("custom legacy target was lost: %q", cfg.Settings.PingTestURL)
	}
	if err := saveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stored), "failover_health_url") {
		t.Fatal("retired target was persisted again")
	}
}

func TestSettingsValidationRejectsInvalidPingPolicy(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*AppSettings)
		field  string
	}{
		{"selection", func(s *AppSettings) { s.PingSelectionMode = "first" }, "ping_selection_mode"},
		{"failover order", func(s *AppSettings) { s.PingFailoverOrder = "reverse" }, "ping_failover_order"},
		{"cache age", func(s *AppSettings) { s.PingCacheMaxAgeMin = 0 }, "ping_cache_max_age_min"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			settings := defaultSettings()
			test.mutate(&settings)
			if err := settings.validate(); err == nil || !strings.Contains(err.Error(), test.field) {
				t.Fatalf("expected %s error, got %v", test.field, err)
			}
		})
	}
}

func TestWriteJSONAtomicReplacesExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"old":true}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONAtomic(path, map[string]bool{"new": true}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "{\n  \"new\": true\n}" {
		t.Fatalf("unexpected file content: %q", data)
	}
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".config.json.tmp-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary files left behind: %v", matches)
	}
}
