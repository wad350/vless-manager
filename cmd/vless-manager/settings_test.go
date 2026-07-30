package main

import (
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

func TestSettingsValidationRejectsUnsafeParallelPing(t *testing.T) {
	settings := defaultSettings()
	settings.PingMaxParallel = 3
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

func TestSettingsValidationRejectsInvalidVPNHealthURL(t *testing.T) {
	settings := defaultSettings()
	settings.FailoverHealthURL = "not a URL"
	if err := settings.validate(); err == nil || !strings.Contains(err.Error(), "failover_health_url") {
		t.Fatalf("expected failover_health_url error, got %v", err)
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
