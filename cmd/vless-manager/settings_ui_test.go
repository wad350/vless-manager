package main

import (
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func TestSettingsUICoversEveryAppSettingExactlyOnce(t *testing.T) {
	appJS, err := webFS.ReadFile("web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	matches := regexp.MustCompile(`(?m)^\s*\{ key: '([a-z0-9_]+)'`).FindAllStringSubmatch(string(appJS), -1)
	uiCounts := make(map[string]int, len(matches))
	for _, match := range matches {
		uiCounts[match[1]]++
	}

	internalSettings := map[string]bool{
		"ping_use_fresh_cache":   true,
		"ping_cache_max_age_min": true,
	}
	settingsType := reflect.TypeOf(AppSettings{})
	for i := 0; i < settingsType.NumField(); i++ {
		tag := strings.Split(settingsType.Field(i).Tag.Get("json"), ",")[0]
		if tag == "" || tag == "-" {
			continue
		}
		if internalSettings[tag] {
			if uiCounts[tag] != 0 {
				t.Errorf("internal setting %q must not appear in SETTINGS_SCHEMA", tag)
			}
			delete(uiCounts, tag)
			continue
		}
		if uiCounts[tag] != 1 {
			t.Errorf("AppSettings.%s (%q) appears %d times in SETTINGS_SCHEMA, want exactly once",
				settingsType.Field(i).Name, tag, uiCounts[tag])
		}
		delete(uiCounts, tag)
	}
	for key, count := range uiCounts {
		t.Errorf("UI setting %q appears %d times but has no AppSettings field", key, count)
	}
}

func TestSettingsUIHasNoSeparateInternetStatusTimers(t *testing.T) {
	appJS, err := webFS.ReadFile("web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"internet_check_interval_sec", "internet_check_timeout_sec"} {
		if strings.Contains(string(appJS), key) {
			t.Errorf("retired setting %q still appears in the UI", key)
		}
	}
}

func TestSubscriptionSettingsUIOwnsRefreshAndServerChecks(t *testing.T) {
	appJS, err := webFS.ReadFile("web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(appJS)
	sections := regexp.MustCompile(`(?m)^    id: '([^']+)',`).FindAllStringSubmatchIndex(js, -1)
	want := map[string]string{
		"subscription_refresh_hours":     "updates",
		"subscription_first_delay_min":   "updates",
		"subscription_fetch_timeout_sec": "updates",
		"subscription_prefer_vpn":        "updates",
		"ping_test_url":                  "updates",
		"ping_timeout_sec":               "updates",
		"ping_max_parallel":              "updates",
		"ping_startup_sleep_ms":          "updates",
		"open_probes":                    "expert",
		"whitelist_probes":               "expert",
	}
	for key, expected := range want {
		position := strings.Index(js, "{ key: '"+key+"'")
		if position < 0 {
			t.Errorf("setting %q is missing", key)
			continue
		}
		actual := ""
		for _, section := range sections {
			if section[0] > position {
				break
			}
			actual = js[section[2]:section[3]]
		}
		if actual != expected {
			t.Errorf("setting %q belongs to section %q, want %q", key, actual, expected)
		}
	}
	for _, label := range []string{
		"Адреса проверки интернета без VPN",
		"Адреса белого списка без VPN",
		"Адрес проверки через VPN",
	} {
		if !strings.Contains(js, label) {
			t.Errorf("missing explicit probe-purpose label %q", label)
		}
	}
	if strings.Contains(js, "key: 'failover_health_url'") {
		t.Error("duplicate VPN health URL control still exists")
	}
}

func TestSettingsWorkspaceHasRequiredControls(t *testing.T) {
	indexHTML, err := webFS.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(indexHTML)
	for _, id := range []string{
		`id="settings-nav"`,
		`id="settings-form"`,
		`id="btn-settings-cancel"`,
		`id="btn-settings-save"`,
	} {
		if !strings.Contains(html, id) {
			t.Errorf("web/index.html is missing %s", id)
		}
	}
}

func TestSettingsUIUsesStandardLogLevelNames(t *testing.T) {
	appJS, err := webFS.ReadFile("web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(appJS)
	for _, level := range []string{"NONE:", "ERROR:", "WARN:", "INFO:", "DEBUG:", "TRACE:"} {
		if !strings.Contains(js, level) {
			t.Errorf("web/app.js is missing standard log level label %q", level)
		}
	}
	if strings.Contains(js, "FATAL:") || strings.Contains(js, "PANIC:") {
		t.Error("Xray settings still show removed log levels")
	}
	if !strings.Contains(js, "Уровень логирования") {
		t.Error("web/app.js is missing the log level field label")
	}
}

func TestEverySettingsItemHasHint(t *testing.T) {
	appJS, err := webFS.ReadFile("web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	schema := string(appJS)
	start := strings.Index(schema, "const SETTINGS_SCHEMA = [")
	if start < 0 {
		t.Fatal("SETTINGS_SCHEMA start not found")
	}
	end := strings.Index(schema[start:], "\n];")
	if end < 0 {
		t.Fatal("SETTINGS_SCHEMA boundaries not found")
	}
	schema = schema[start : start+end]

	itemPattern := regexp.MustCompile(`(?m)^\s*\{ key: '([a-z0-9_]+)'`)
	matches := itemPattern.FindAllStringSubmatchIndex(schema, -1)
	for i, match := range matches {
		itemEnd := len(schema)
		if i+1 < len(matches) {
			itemEnd = matches[i+1][0]
		}
		if !strings.Contains(schema[match[0]:itemEnd], "hint:") {
			key := schema[match[2]:match[3]]
			t.Errorf("setting %q has no hint", key)
		}
	}
}
