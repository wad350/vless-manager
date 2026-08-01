package main

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

// AppSettings holds every tunable that used to be a hardcoded constant.
// Stored inside Config (persists to config.json). Fields are int seconds /
// human-readable units so the web UI can render numeric inputs directly.
//
// Add new fields here; provide a sane default in defaultSettings(); reference
// them through (*Config).effectiveSettings() so a half-populated user config
// transparently inherits the rest.
type AppSettings struct {
	// --- Web access ---
	// Credentials are never stored here. When enabled, the login form verifies
	// the supplied credentials against Keenetic's own /auth endpoint.
	AuthEnabled         bool `json:"auth_enabled"`
	AuthSessionTTLHours int  `json:"auth_session_ttl_hours"`

	// --- Failover controller ---
	FailoverOuterIntervalSec  int    `json:"failover_outer_interval_sec"`  // outer probes period
	FailoverHealthIntervalSec int    `json:"failover_health_interval_sec"` // VPN-through probe period
	FailoverHysteresis        int    `json:"failover_hysteresis"`          // consecutive outer-decisions before flip
	FailoverProbeTimeoutSec   int    `json:"failover_probe_timeout_sec"`   // per-probe HTTP timeout
	FailoverVPNSwapAfterFails int    `json:"failover_vpn_swap_after_fails"`
	FailoverStartBackoffSec   int    `json:"failover_start_backoff_sec"`
	FailoverHealthTimeoutSec  int    `json:"failover_health_timeout_sec"` // SOCKS probe timeout via VPN
	FailoverHealthURL         string `json:"failover_health_url"`         // endpoint checked through VPN

	// Probe URL sets — exposed so a user behind a custom whitelist can
	// override which sites count as "outer" (broadly internet) vs
	// "whitelist" (RU operator zero-rated).
	OpenProbes      []string `json:"open_probes"`
	WhitelistProbes []string `json:"whitelist_probes"`

	// --- Subscriptions ---
	SubscriptionRefreshHours    int `json:"subscription_refresh_hours"`
	SubscriptionFetchTimeoutSec int `json:"subscription_fetch_timeout_sec"`
	SubscriptionFirstDelayMin   int `json:"subscription_first_delay_min"` // delay after boot before first refresh
	// SubscriptionPreferVPN uses the running main tunnel only after its
	// health probe succeeds. Otherwise subscription fetches bypass TUN.
	SubscriptionPreferVPN bool `json:"subscription_prefer_vpn"`

	// --- Boot / WAN ---
	WaitForWANSec            int `json:"wait_for_wan_sec"`            // hard cap at boot waiting for default route
	InternetCheckIntervalSec int `json:"internet_check_interval_sec"` // background WAN health refresh
	InternetCheckTimeoutSec  int `json:"internet_check_timeout_sec"`  // per-URL probe timeout

	// --- Ping ---
	PingTimeoutSec int    `json:"ping_timeout_sec"`
	PingTestURL    string `json:"ping_test_url"`
	// PingMaxParallel caps parallel temporary sing-box instances. 0 or 1 is
	// sequential; 2 is the validated maximum for this 124 MB router.
	PingMaxParallel    int `json:"ping_max_parallel"`
	PingStartupSleepMS int `json:"ping_startup_sleep_ms"` // wait for SOCKS listener inside temp sing-box
	// Selection mode is priority (lowest latency in the first subscription
	// containing a working node) or fastest (lowest latency globally).
	PingSelectionMode string `json:"ping_selection_mode"`
	// A complete, fresh cache can satisfy a start selection without network
	// probes. Partial/stale groups are probed normally.
	PingUseFreshCache  bool `json:"ping_use_fresh_cache"`
	PingCacheMaxAgeMin int  `json:"ping_cache_max_age_min"`
	// Failover order: active_first|priority.
	PingFailoverOrder string `json:"ping_failover_order"`

	// --- sing-box ---
	LogLevel string `json:"log_level"` // error|warn|info|debug|trace
	// ServiceLogLevel controls vless-manager's own operational log. At debug
	// it includes every ping and subscription download attempt.
	ServiceLogLevel string `json:"service_log_level"` // error|warn|info|debug|trace

	// --- Bypass routing ---
	// BypassRouteRussia toggles the embedded ~900-entry RU operator whitelist
	// (ya.ru, mail.ru, vk.ru, gosuslugi, Sberbank, Yandex/VK CDNs, etc.) as
	// `domain_suffix` rules going to the `direct` outbound. sing-box uses
	// sniff'd SNI/Host so the match happens before any VPN tunnel work.
	BypassRouteRussia bool `json:"bypass_route_russia"`
	// BypassDomains is the user-editable additional list. Each entry is a
	// domain_suffix (so `example.com` matches `a.example.com` too). One
	// domain per line in the UI textarea.
	BypassDomains []string `json:"bypass_domains"`
}

// defaultSettings returns a brand-new AppSettings populated with the same
// values the code used to bake in as compile-time constants. Loading a
// pre-Settings config.json transparently picks these up.
func defaultSettings() AppSettings {
	return AppSettings{
		AuthEnabled:         false,
		AuthSessionTTLHours: 24,

		FailoverOuterIntervalSec:  30,
		FailoverHealthIntervalSec: 10,
		FailoverHysteresis:        2,
		FailoverProbeTimeoutSec:   4,
		FailoverVPNSwapAfterFails: 2,
		FailoverStartBackoffSec:   300,
		FailoverHealthTimeoutSec:  6,
		FailoverHealthURL:         "http://cp.cloudflare.com/generate_204",
		OpenProbes: []string{
			"http://cp.cloudflare.com/generate_204",
			"http://detectportal.firefox.com/success.txt",
			"http://www.google.com/generate_204",
		},
		WhitelistProbes: []string{
			"http://ya.ru",
			"http://mail.ru",
			"http://vk.com",
		},

		SubscriptionRefreshHours:    1,
		SubscriptionFetchTimeoutSec: 15,
		SubscriptionFirstDelayMin:   10,
		SubscriptionPreferVPN:       true,

		WaitForWANSec:            180,
		InternetCheckIntervalSec: 3600,
		InternetCheckTimeoutSec:  5,

		PingTimeoutSec:     30,
		PingTestURL:        "http://www.gstatic.com/generate_204",
		PingMaxParallel:    0,
		PingStartupSleepMS: 300,
		PingSelectionMode:  "priority",
		PingUseFreshCache:  false,
		PingCacheMaxAgeMin: 60,
		PingFailoverOrder:  "active_first",

		LogLevel:        "error",
		ServiceLogLevel: "info",

		BypassRouteRussia: true,
		BypassDomains:     []string{},
	}
}

func (s AppSettings) validate() error {
	type intRule struct {
		name     string
		value    int
		min, max int
	}
	rules := []intRule{
		{"auth_session_ttl_hours", s.AuthSessionTTLHours, 1, 720},
		{"failover_outer_interval_sec", s.FailoverOuterIntervalSec, 5, 3600},
		{"failover_health_interval_sec", s.FailoverHealthIntervalSec, 10, 3600},
		{"failover_hysteresis", s.FailoverHysteresis, 1, 20},
		{"failover_probe_timeout_sec", s.FailoverProbeTimeoutSec, 1, 120},
		{"failover_vpn_swap_after_fails", s.FailoverVPNSwapAfterFails, 1, 100},
		{"failover_start_backoff_sec", s.FailoverStartBackoffSec, 10, 86400},
		{"failover_health_timeout_sec", s.FailoverHealthTimeoutSec, 1, 120},
		{"subscription_refresh_hours", s.SubscriptionRefreshHours, 1, 168},
		{"subscription_fetch_timeout_sec", s.SubscriptionFetchTimeoutSec, 3, 300},
		{"subscription_first_delay_min", s.SubscriptionFirstDelayMin, 0, 1440},
		{"wait_for_wan_sec", s.WaitForWANSec, 10, 1800},
		{"internet_check_interval_sec", s.InternetCheckIntervalSec, 30, 86400},
		{"internet_check_timeout_sec", s.InternetCheckTimeoutSec, 1, 120},
		{"ping_timeout_sec", s.PingTimeoutSec, 3, 120},
		{"ping_max_parallel", s.PingMaxParallel, 0, 2},
		{"ping_startup_sleep_ms", s.PingStartupSleepMS, 50, 5000},
		{"ping_cache_max_age_min", s.PingCacheMaxAgeMin, 1, 1440},
	}
	for _, rule := range rules {
		if rule.value < rule.min || rule.value > rule.max {
			return fmt.Errorf("%s must be between %d and %d", rule.name, rule.min, rule.max)
		}
	}
	if err := validateHTTPURL("ping_test_url", s.PingTestURL); err != nil {
		return err
	}
	if err := validateHTTPURL("failover_health_url", s.FailoverHealthURL); err != nil {
		return err
	}
	if err := validateProbeURLs("open_probes", s.OpenProbes); err != nil {
		return err
	}
	if err := validateProbeURLs("whitelist_probes", s.WhitelistProbes); err != nil {
		return err
	}
	if !oneOf(s.LogLevel, "panic", "fatal", "error", "warn", "info", "debug", "trace") {
		return fmt.Errorf("invalid log_level %q", s.LogLevel)
	}
	if !oneOf(s.ServiceLogLevel, "error", "warn", "info", "debug", "trace") {
		return fmt.Errorf("invalid service_log_level %q", s.ServiceLogLevel)
	}
	if !oneOf(s.PingSelectionMode, "priority", "fastest") {
		return fmt.Errorf("invalid ping_selection_mode %q", s.PingSelectionMode)
	}
	if !oneOf(s.PingFailoverOrder, "active_first", "priority") {
		return fmt.Errorf("invalid ping_failover_order %q", s.PingFailoverOrder)
	}
	if len(s.BypassDomains) > 1000 {
		return fmt.Errorf("bypass_domains contains more than 1000 entries")
	}
	for _, domain := range s.BypassDomains {
		domain = strings.TrimSpace(domain)
		if domain == "" || strings.ContainsAny(domain, " /:@") {
			return fmt.Errorf("invalid bypass domain %q", domain)
		}
	}
	return nil
}

func validateProbeURLs(name string, values []string) error {
	if len(values) == 0 {
		return fmt.Errorf("%s must contain at least one URL", name)
	}
	if len(values) > 20 {
		return fmt.Errorf("%s contains more than 20 URLs", name)
	}
	for _, value := range values {
		if err := validateHTTPURL(name, value); err != nil {
			return err
		}
	}
	return nil
}

func validateHTTPURL(name, value string) error {
	parsed, err := url.ParseRequestURI(strings.TrimSpace(value))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return fmt.Errorf("%s contains invalid HTTP URL %q", name, value)
	}
	return nil
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

// fillDefaults patches every zero-value field on s with the matching default.
// Returns true when any field was changed (caller persists the patched
// config so the user sees explicit values in the JSON).
func (s *AppSettings) fillDefaults() bool {
	d := defaultSettings()
	changed := false
	if s.AuthSessionTTLHours <= 0 {
		s.AuthSessionTTLHours = d.AuthSessionTTLHours
		changed = true
	}
	if s.FailoverOuterIntervalSec <= 0 {
		s.FailoverOuterIntervalSec = d.FailoverOuterIntervalSec
		changed = true
	}
	if s.FailoverHealthIntervalSec <= 0 {
		s.FailoverHealthIntervalSec = d.FailoverHealthIntervalSec
		changed = true
	}
	if s.FailoverHysteresis <= 0 {
		s.FailoverHysteresis = d.FailoverHysteresis
		changed = true
	}
	if s.FailoverProbeTimeoutSec <= 0 {
		s.FailoverProbeTimeoutSec = d.FailoverProbeTimeoutSec
		changed = true
	}
	if s.FailoverVPNSwapAfterFails <= 0 {
		s.FailoverVPNSwapAfterFails = d.FailoverVPNSwapAfterFails
		changed = true
	}
	if s.FailoverStartBackoffSec <= 0 {
		s.FailoverStartBackoffSec = d.FailoverStartBackoffSec
		changed = true
	}
	if s.FailoverHealthTimeoutSec <= 0 {
		s.FailoverHealthTimeoutSec = d.FailoverHealthTimeoutSec
		changed = true
	}
	if len(s.OpenProbes) == 0 {
		s.OpenProbes = append([]string(nil), d.OpenProbes...)
		changed = true
	}
	if len(s.WhitelistProbes) == 0 {
		s.WhitelistProbes = append([]string(nil), d.WhitelistProbes...)
		changed = true
	}
	if s.SubscriptionRefreshHours <= 0 {
		s.SubscriptionRefreshHours = d.SubscriptionRefreshHours
		changed = true
	}
	if s.SubscriptionFetchTimeoutSec <= 0 {
		s.SubscriptionFetchTimeoutSec = d.SubscriptionFetchTimeoutSec
		changed = true
	}
	if s.SubscriptionFirstDelayMin < 0 {
		s.SubscriptionFirstDelayMin = d.SubscriptionFirstDelayMin
		changed = true
	}
	if s.WaitForWANSec <= 0 {
		s.WaitForWANSec = d.WaitForWANSec
		changed = true
	}
	if s.InternetCheckIntervalSec <= 0 {
		s.InternetCheckIntervalSec = d.InternetCheckIntervalSec
		changed = true
	}
	if s.InternetCheckTimeoutSec <= 0 {
		s.InternetCheckTimeoutSec = d.InternetCheckTimeoutSec
		changed = true
	}
	if s.PingTimeoutSec <= 0 {
		s.PingTimeoutSec = d.PingTimeoutSec
		changed = true
	}
	if s.PingTestURL == "" {
		s.PingTestURL = d.PingTestURL
		changed = true
	}
	if s.FailoverHealthURL == "" {
		s.FailoverHealthURL = d.FailoverHealthURL
		changed = true
	}
	if s.PingStartupSleepMS <= 0 {
		s.PingStartupSleepMS = d.PingStartupSleepMS
		changed = true
	}
	if s.PingSelectionMode == "" {
		s.PingSelectionMode = d.PingSelectionMode
		changed = true
	}
	// Cached ping results are retained for status display only. Connection and
	// failover decisions always verify candidates with a fresh probe.
	if s.PingUseFreshCache {
		s.PingUseFreshCache = false
		changed = true
	}
	if s.PingCacheMaxAgeMin <= 0 {
		s.PingCacheMaxAgeMin = d.PingCacheMaxAgeMin
		changed = true
	}
	if s.PingFailoverOrder == "" {
		s.PingFailoverOrder = d.PingFailoverOrder
		changed = true
	}
	if s.LogLevel == "" {
		s.LogLevel = d.LogLevel
		changed = true
	}
	if s.ServiceLogLevel == "" {
		s.ServiceLogLevel = d.ServiceLogLevel
		changed = true
	}
	return changed
}

// -- Convenience accessors with sane fallbacks --
//
// Reading via these keeps call-sites short and removes per-site nil/zero checks.

func (s AppSettings) OuterInterval() time.Duration {
	return time.Duration(s.FailoverOuterIntervalSec) * time.Second
}
func (s AppSettings) HealthInterval() time.Duration {
	return time.Duration(s.FailoverHealthIntervalSec) * time.Second
}
func (s AppSettings) ProbeTimeout() time.Duration {
	return time.Duration(s.FailoverProbeTimeoutSec) * time.Second
}
func (s AppSettings) StartBackoff() time.Duration {
	return time.Duration(s.FailoverStartBackoffSec) * time.Second
}
func (s AppSettings) HealthTimeout() time.Duration {
	return time.Duration(s.FailoverHealthTimeoutSec) * time.Second
}
func (s AppSettings) WaitForWANTimeout() time.Duration {
	return time.Duration(s.WaitForWANSec) * time.Second
}
func (s AppSettings) InternetCheckInterval() time.Duration {
	return time.Duration(s.InternetCheckIntervalSec) * time.Second
}
func (s AppSettings) InternetCheckTimeout() time.Duration {
	return time.Duration(s.InternetCheckTimeoutSec) * time.Second
}
func (s AppSettings) PingTimeout() time.Duration {
	return time.Duration(s.PingTimeoutSec) * time.Second
}
func (s AppSettings) SubscriptionRefreshInterval() time.Duration {
	return time.Duration(s.SubscriptionRefreshHours) * time.Hour
}
func (s AppSettings) SubscriptionFetchTimeout() time.Duration {
	return time.Duration(s.SubscriptionFetchTimeoutSec) * time.Second
}
func (s AppSettings) PingStartupSleep() time.Duration {
	return time.Duration(s.PingStartupSleepMS) * time.Millisecond
}
func (s AppSettings) PingCacheMaxAge() time.Duration {
	return time.Duration(s.PingCacheMaxAgeMin) * time.Minute
}
