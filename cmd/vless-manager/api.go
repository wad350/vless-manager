package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// apiServer is the REST front-end. All mutable shared state (cfg, subs) is
// guarded by `mu`. Read-only handlers take RLock; mutation handlers take Lock.
// Slow operations (ping tests, sing-box start/stop) must snapshot under lock
// then release before doing the slow work, otherwise the UI status poll (2 s)
// blocks for tens of seconds.
// PingProgress reports the live state of a sequential ping run so the UI
// (and /api/status) can show "3/7: тестирую Gold Россия".
type PingProgress struct {
	Running   bool      `json:"running"`
	Done      int       `json:"done"`
	Total     int       `json:"total"`
	Group     string    `json:"group,omitempty"` // subscription/group being tested
	Current   string    `json:"current"`         // server name currently being tested
	StartedAt time.Time `json:"started_at"`
	Reachable int       `json:"reachable"` // running count of successes
	Incompat  int       `json:"incompatible"`
	Profiles  int       `json:"profiles_total"` // logical profiles represented by the physical endpoints
}

type priorityServerGroup struct {
	ID      string
	Name    string
	Servers []VLESSServer
}

type apiServer struct {
	mu sync.RWMutex

	pm        *ProcessManager
	cfg       *Config
	subs      []*Subscription
	cfgPath   string
	subPath   string
	mux       *http.ServeMux
	health    *healthMonitor
	pingCache *pingCache
	failover  *failoverController
	updater   *appUpdater

	pingMu       sync.Mutex
	pingProgress PingProgress
	pingGen      uint64
	pingStopGen  uint64
	pingRunID    uint64
	pingCancel   context.CancelFunc
	lastPing     []PingResult
	bypassDiag   bypassDiagnostics
	// pingRunner is overridden only by unit tests for priority selection.
	// Production calls runPingAll.
	pingRunner func([]VLESSServer) []PingResult
	// connectStartFn is overridden by tests. Production always starts VPN when
	// the user selects Connect on a server.
	connectStartFn func(*Config) error

	// pingRunMu serialises full ping cycles globally — prevents two parallel
	// probe batches from overwhelming the modem on the 124 MB router.
	pingRunMu sync.Mutex
}

func newAPIServer(pm *ProcessManager, cfg *Config, subs []*Subscription, cfgPath, subPath string) *apiServer {
	s := &apiServer{
		pm: pm, cfg: cfg, subs: subs, cfgPath: cfgPath, subPath: subPath,
		mux:       http.NewServeMux(),
		health:    newHealthMonitor(),
		pingCache: newPingCache(filepath.Join(filepath.Dir(cfgPath), "ping-cache.json")),
	}
	s.failover = newFailoverController(s, cfg.AutoFailover, cfg.AutoTunnelFailover)
	s.updater = newAppUpdater(s)
	s.connectStartFn = s.startManagedVPN
	s.health.SetSettingsSource(s.settingsSnapshot)
	s.routes()
	return s
}

// settingsSnapshot returns a defensive copy of the currently-loaded
// AppSettings. All long-running goroutines (failover, refresh loop, health
// monitor) call this on every tick so a /api/settings PATCH takes effect
// without restarting the process.
//
// Mutex: takes RLock briefly. The returned struct value-copies slices so the
// caller can iterate without holding the lock. Cheap (~few hundred bytes).
func (s *apiServer) settingsSnapshot() AppSettings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := s.cfg.Settings
	out.OpenProbes = append([]string(nil), s.cfg.Settings.OpenProbes...)
	out.WhitelistProbes = append([]string(nil), s.cfg.Settings.WhitelistProbes...)
	return out
}

// runPingAll is the entry point for all ping cycles. The requested slice
// may have been captured while another cycle was running, so enabled servers
// are checked again after acquiring pingRunMu.
func (s *apiServer) runPingAll(servers []VLESSServer) []PingResult {
	return s.runPingAllNamed(servers, "")
}

func (s *apiServer) runPingAllNamed(servers []VLESSServer, group string) []PingResult {
	if len(servers) == 0 {
		return nil
	}

	// Wait for an in-flight cycle. If it covered every requested server, reuse
	// those freshly completed results. Never return an older cache snapshot:
	// /start must not connect before every enabled server has been tested.
	s.pingMu.Lock()
	observedGen := s.pingGen
	observedStopGen := s.pingStopGen
	s.pingMu.Unlock()
	s.pingRunMu.Lock()
	defer s.pingRunMu.Unlock()

	s.pingMu.Lock()
	if s.pingStopGen != observedStopGen {
		s.pingMu.Unlock()
		return nil
	}
	s.pingRunID++
	runID := s.pingRunID
	ctx, cancel := context.WithCancel(context.Background())
	s.pingCancel = cancel
	s.pingMu.Unlock()
	defer func() {
		cancel()
		s.pingMu.Lock()
		if s.pingRunID == runID {
			s.pingCancel = nil
			s.pingProgress = PingProgress{}
		}
		s.pingMu.Unlock()
	}()

	servers = s.enabledServersFrom(servers)
	if len(servers) == 0 {
		return nil
	}
	pingSettings := s.settingsSnapshot()

	s.pingMu.Lock()
	if s.pingGen != observedGen {
		if results, ok := pingResultsForServers(s.lastPing, servers); ok {
			s.pingMu.Unlock()
			s.pm.log(serviceLogDebug, "[ping] использую только что завершённый полный проход")
			return results
		}
	}
	s.pingMu.Unlock()

	s.pingMu.Lock()
	s.pingProgress = PingProgress{
		Running:   true,
		Total:     pingWorkUnitCount(servers),
		Profiles:  len(servers),
		Group:     group,
		StartedAt: time.Now(),
	}
	s.pingMu.Unlock()

	// WAN bypass rules so temp sing-box connections hit the VLESS servers
	// directly, not through the currently-active tunnel.
	seen := map[string]bool{}
	for _, srv := range physicalPingServers(servers) {
		for _, ip := range ResolveAddrs(srv.Address) {
			if !seen[ip] {
				seen[ip] = true
				AddPingBypass(ip)
			}
		}
	}
	defer ClearPingBypasses()

	opID := s.pm.nextOperationID("ping")
	started := time.Now()
	effectiveParallel, parallelReason := effectivePingParallel(pingSettings.PingMaxParallel, deviceMemoryKB())
	s.pm.event(serviceLogInfo, "ping", "batch.start",
		"проверка серверов начата",
		field("op_id", opID),
		field("servers", len(servers)),
		field("parallel_requested", pingSettings.PingMaxParallel),
		field("parallel_effective", effectiveParallel),
		field("parallel_limit_reason", parallelReason),
		field("go_cpu_limit", runtimeCPULimit),
		field("timeout_ms", pingSettings.PingTimeout().Milliseconds()),
		field("test_url", pingSettings.PingTestURL))

	results := make([]PingResult, len(servers))
	incompat := 0

	// Flag incompatible servers upfront; collect compatible ones for the batch.
	compatible := make([]VLESSServer, 0, len(servers))
	compatIdx := make([]int, 0, len(servers))
	for i, srv := range servers {
		results[i] = PingResult{
			ServerID:   srv.ID,
			ServerName: srv.Name,
			Address:    srv.Address,
			Port:       srv.Port,
			Protocol:   describeProtocol(&srv),
			LatencyMS:  -1,
			CheckedAt:  time.Now(),
		}
		if !isSupportedServer(&srv) {
			results[i].Incompat = true
			results[i].Error = "transport not supported by sing-box: " + srv.Network
			incompat++
			s.pm.event(serviceLogDebug, "ping", "server.incompatible",
				"сервер пропущен: транспорт не поддерживается",
				field("op_id", opID),
				field("server", srv.Name),
				field("transport", srv.Network))
		} else {
			compatible = append(compatible, srv)
			compatIdx = append(compatIdx, i)
		}
	}

	s.pingMu.Lock()
	s.pingProgress.Done = incompat
	s.pingProgress.Incompat = incompat
	s.pingMu.Unlock()

	reachable, unreachable := 0, 0

	if len(compatible) > 0 {
		// onDone is called per-server as goroutines complete inside the batch.
		onDone := func(j int, r PingResult) {
			s.pingCache.Set(r)
			if r.LatencyMS >= 0 {
				s.pm.event(serviceLogDebug, "ping", "server.succeeded",
					"сервер доступен",
					field("op_id", opID),
					field("server", r.ServerName),
					field("address", r.Address),
					field("latency_ms", r.LatencyMS))
			} else {
				errMsg := r.Error
				s.pm.event(serviceLogDebug, "ping", "server.failed",
					"сервер недоступен",
					field("op_id", opID),
					field("server", r.ServerName),
					field("address", r.Address),
					field("error", errMsg))
			}
			s.pingMu.Lock()
			s.pingProgress.Done++
			s.pingProgress.Current = r.ServerName
			if r.LatencyMS >= 0 {
				s.pingProgress.Reachable++
			}
			s.pingMu.Unlock()
		}

		st := pingSettings
		timeout := st.PingTimeout()
		if timeout <= 0 {
			timeout = 30 * time.Second
		}
		pingStartupWait = st.PingStartupSleep()
		batchResults := pingBatchViaSingBoxContext(ctx, compatible, timeout, st.PingTestURL, effectiveParallel, onDone)
		if ctx.Err() != nil {
			s.pm.event(serviceLogInfo, "ping", "batch.cancelled",
				"проверка серверов отменена",
				field("op_id", opID),
				field("completed", s.pingProgressSnapshot().Done),
				field("servers", len(servers)),
				field("duration_ms", time.Since(started).Milliseconds()))
			return nil
		}

		for j, i := range compatIdx {
			r := batchResults[j]
			r.Protocol = results[i].Protocol
			r.CheckedAt = time.Now()
			results[i] = r
			if r.LatencyMS >= 0 {
				reachable++
			} else {
				unreachable++
			}
		}
	}

	sortByLatency(results)
	s.pingCache.Update(results)
	s.pingMu.Lock()
	s.lastPing = append([]PingResult(nil), results...)
	s.pingGen++
	s.pingMu.Unlock()
	s.pm.event(serviceLogInfo, "ping", "batch.completed",
		"проверка серверов завершена",
		field("op_id", opID),
		field("reachable", reachable),
		field("unreachable", unreachable),
		field("incompatible", incompat),
		field("duration_ms", time.Since(started).Milliseconds()))
	return results
}

func (s *apiServer) pingProgressSnapshot() PingProgress {
	s.pingMu.Lock()
	defer s.pingMu.Unlock()
	return s.pingProgress
}

// cancelPingRun invalidates both the active probe batch and requests waiting
// for pingRunMu. A cancelled Start request must never start VPN afterwards.
func (s *apiServer) cancelPingRun(reason string) {
	s.pingMu.Lock()
	s.pingStopGen++
	cancel := s.pingCancel
	running := s.pingProgress.Running
	s.pingMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if running {
		s.pm.event(serviceLogInfo, "ping", "batch.cancel_requested",
			"запрошена отмена проверки серверов",
			field("reason", reason))
	}
}

func (s *apiServer) pingStopGeneration() uint64 {
	s.pingMu.Lock()
	defer s.pingMu.Unlock()
	return s.pingStopGen
}

func (s *apiServer) pingStoppedSince(generation uint64) bool {
	return s.pingStopGeneration() != generation
}

func deviceMemoryKB() int64 {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		var total int64
		if _, err := fmt.Sscanf(line, "MemTotal: %d kB", &total); err == nil {
			return total
		}
	}
	return 0
}

func effectivePingParallel(requested int, totalMemoryKB int64) (int, string) {
	if requested <= 1 {
		return 1, ""
	}
	if totalMemoryKB > 0 && totalMemoryKB < 256*1024 {
		return 1, "memory_below_256mb"
	}
	return requested, ""
}

func (s *apiServer) pingServers(servers []VLESSServer) []PingResult {
	return s.pingServersNamed(servers, "")
}

func (s *apiServer) pingServersNamed(servers []VLESSServer, group string) []PingResult {
	if s.pingRunner != nil {
		return s.pingRunner(servers)
	}
	return s.runPingAllNamed(servers, group)
}

func (s *apiServer) freshCachedResults(servers []VLESSServer, maxAge time.Duration) ([]PingResult, bool) {
	if len(servers) == 0 || maxAge <= 0 {
		return nil, false
	}
	cutoff := time.Now().Add(-maxAge)
	results := make([]PingResult, 0, len(servers))
	for _, server := range servers {
		result, ok := s.pingCache.Get(server.ID)
		if !ok || result.CheckedAt.Before(cutoff) {
			return nil, false
		}
		results = append(results, result)
	}
	sortByLatency(results)
	return results, true
}

// freshReachableCachedResults returns the usable subset of a partially
// populated cache. Start and failover prefer a known-good node over forcing a
// full real-tunnel scan merely because some subscription nodes were never
// tested or an earlier scan was cancelled.
func (s *apiServer) freshReachableCachedResults(servers []VLESSServer, maxAge time.Duration) ([]PingResult, bool) {
	if len(servers) == 0 || maxAge <= 0 {
		return nil, false
	}
	cutoff := time.Now().Add(-maxAge)
	results := make([]PingResult, 0, len(servers))
	for _, server := range servers {
		result, ok := s.pingCache.Get(server.ID)
		if !ok || result.CheckedAt.Before(cutoff) || result.LatencyMS < 0 || result.Incompat {
			continue
		}
		results = append(results, result)
	}
	sortByLatency(results)
	return results, len(results) > 0
}

func (s *apiServer) enabledServersFrom(requested []VLESSServer) []VLESSServer {
	s.mu.RLock()
	enabled := s.allServersLocked()
	s.mu.RUnlock()

	enabledIDs := make(map[string]struct{}, len(enabled))
	for _, server := range enabled {
		enabledIDs[server.ID] = struct{}{}
	}
	filtered := make([]VLESSServer, 0, len(requested))
	seen := make(map[string]struct{}, len(requested))
	for _, server := range requested {
		if _, ok := enabledIDs[server.ID]; !ok {
			continue
		}
		if _, duplicate := seen[server.ID]; duplicate {
			continue
		}
		seen[server.ID] = struct{}{}
		filtered = append(filtered, server)
	}
	return filtered
}

func pingResultsForServers(results []PingResult, servers []VLESSServer) ([]PingResult, bool) {
	byID := make(map[string]PingResult, len(results))
	for _, result := range results {
		byID[result.ServerID] = result
	}
	out := make([]PingResult, 0, len(servers))
	for _, server := range servers {
		result, ok := byID[server.ID]
		if !ok {
			return nil, false
		}
		out = append(out, result)
	}
	sortByLatency(out)
	return out, true
}

// startVPNInternal is the callable version of /api/start used by failover.
func (s *apiServer) startVPNInternal() error {
	if _, err := s.prepareServerForStart(); err != nil {
		return err
	}
	s.mu.RLock()
	cfgSnap := *s.cfg
	s.mu.RUnlock()
	if cfgSnap.ActiveServer == "" {
		return fmt.Errorf("no active server")
	}
	return s.startManagedVPN(&cfgSnap)
}

// startManagedVPN is the single successful-start path. Every caller gets an
// immediate tunnel health-check, including boot autostart, reconnect and
// manual server selection.
func (s *apiServer) startManagedVPN(cfg *Config) error {
	if err := s.pm.Start(cfg); err != nil {
		return err
	}
	if s.failover != nil {
		s.failover.CheckTunnelNow()
	}
	return nil
}

// chooseAlternativeServer follows the configured failover subscription order.
func (s *apiServer) chooseAlternativeServer() string {
	s.mu.RLock()
	activeID := s.cfg.ActiveServer
	s.mu.RUnlock()

	st := s.settingsSnapshot()
	rotate := st.PingFailoverOrder == "active_first"
	s.pm.event(serviceLogInfo, "priority", "failover.selection_started",
		"начат поиск замены нерабочему серверу",
		field("active_server_id", activeID),
		field("selection_mode", st.PingSelectionMode),
		field("subscription_order", st.PingFailoverOrder))
	// Diagnostic cache is never authoritative for failover: a node that was
	// reachable minutes ago may be down now. Probe the configured priority
	// groups again and switch only to a server proven reachable in this run.
	server, result := s.findBestConfigured(activeID, rotate, false)
	if server == nil || result == nil {
		s.pm.event(serviceLogWarn, "priority", "failover.selection_failed",
			"рабочая замена не найдена",
			field("active_server_id", activeID),
			field("selection_mode", st.PingSelectionMode),
			field("subscription_order", st.PingFailoverOrder))
		return ""
	}
	s.commitSelectedServer(preferGroupMember(*server, result.SelectedMemberID))
	s.pm.event(serviceLogInfo, "priority", "failover.selection_succeeded",
		"выбрана замена нерабочему серверу",
		field("previous_server_id", activeID),
		field("server", result.ServerName),
		field("latency_ms", result.LatencyMS))
	return result.ServerName
}

func (s *apiServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *apiServer) routes() {
	s.mux.HandleFunc("/api/status", s.handleStatus)
	s.mux.HandleFunc("/api/config", s.handleConfig)
	s.mux.HandleFunc("/api/start", s.handleStart)
	s.mux.HandleFunc("/api/stop", s.handleStop)
	s.mux.HandleFunc("/api/restart", s.handleRestart)
	s.mux.HandleFunc("/api/logs", s.handleLogs)
	// Manual servers
	s.mux.HandleFunc("/api/servers", s.handleServers)
	s.mux.HandleFunc("/api/servers/", s.handleServerByID)
	// Subscriptions
	s.mux.HandleFunc("/api/subscriptions", s.handleSubscriptions)
	s.mux.HandleFunc("/api/subscriptions/", s.handleSubscriptionByID)
	// Ping / auto-select
	s.mux.HandleFunc("/api/ping", s.handlePing)
	s.mux.HandleFunc("/api/ping/auto-select", s.handlePingAutoSelect)
	// Parse URI
	s.mux.HandleFunc("/api/parse-uri", s.handleParseURI)
	// Traffic stats (tun0 rx/tx bytes since interface up)
	s.mux.HandleFunc("/api/traffic", s.handleTraffic)
	// Version info (manager + bundled sing-box)
	s.mux.HandleFunc("/api/version", s.handleVersion)
	// Application updates from signed-by-hash GitHub Release assets.
	s.mux.HandleFunc("/api/update", s.handleUpdate)
	s.mux.HandleFunc("/api/update/check", s.handleUpdateCheck)
	s.mux.HandleFunc("/api/update/install", s.handleUpdateInstall)
	// On-demand WAN health probe (manual trigger from UI)
	s.mux.HandleFunc("/api/internet/check", s.handleInternetCheck)
	// Auto-failover state / toggle
	s.mux.HandleFunc("/api/failover", s.handleFailover)
	// App-wide tunables (timings, probe URLs, log level, ...). GET returns
	// the live values; PATCH merges a partial JSON body into the stored
	// config. Defaults are exposed via /api/settings/defaults so the UI can
	// render a reset button without round-tripping back to compiled-in
	// fallbacks.
	s.mux.HandleFunc("/api/settings", s.handleSettings)
	s.mux.HandleFunc("/api/settings/defaults", s.handleSettingsDefaults)
	// Runtime-refreshable copy of the embedded RU bypass whitelist.
	s.mux.HandleFunc("/api/bypass", s.handleBypass)
}

// handleSettings:
//
//	GET   → current AppSettings
//	PATCH → merge fields from request body, save, return new AppSettings
//
// PATCH merges only the keys present in the body (everything else stays as
// it was). Empty arrays sent explicitly clear the corresponding probe list,
// which is then re-filled with defaults on the next fillDefaults pass — that
// reset gesture is how the UI restores the stock probe URLs.
func (s *apiServer) handleSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, s.settingsSnapshot())
	case http.MethodPatch, http.MethodPost:
		var patch map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		s.mu.Lock()
		previous := s.cfg.Settings
		merged := s.cfg.Settings
		// Re-encode merged + patch overlay so unknown keys are ignored.
		base, _ := json.Marshal(merged)
		// Decode base into a map, then overlay patch keys, then re-encode.
		var asMap map[string]json.RawMessage
		_ = json.Unmarshal(base, &asMap)
		if asMap == nil {
			asMap = map[string]json.RawMessage{}
		}
		for k, v := range patch {
			if _, ok := asMap[k]; !ok {
				s.mu.Unlock()
				writeError(w, http.StatusBadRequest, "unknown setting: "+k)
				return
			}
			asMap[k] = v
		}
		mergedJSON, _ := json.Marshal(asMap)
		var out AppSettings
		if err := json.Unmarshal(mergedJSON, &out); err != nil {
			s.mu.Unlock()
			writeError(w, http.StatusBadRequest, "invalid settings: "+err.Error())
			return
		}
		out.fillDefaults()
		if err := out.validate(); err != nil {
			s.mu.Unlock()
			writeError(w, http.StatusBadRequest, "invalid settings: "+err.Error())
			return
		}
		s.cfg.Settings = out
		if err := saveConfig(s.cfgPath, s.cfg); err != nil {
			s.cfg.Settings = previous
			s.mu.Unlock()
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		snap := s.cfg.Settings
		cfgSnap := *s.cfg
		s.mu.Unlock()
		s.pm.SetServiceLogLevel(out.ServiceLogLevel)
		s.failover.ReloadSettings()

		restartRequired := previous.BypassRouteRussia != out.BypassRouteRussia ||
			!slices.Equal(previous.BypassDomains, out.BypassDomains) ||
			previous.LogLevel != out.LogLevel
		s.pm.event(serviceLogInfo, "manager", "settings.updated",
			"настройки сохранены",
			field("service_log_level", out.ServiceLogLevel),
			field("singbox_log_level", out.LogLevel),
			field("ping_selection_mode", out.PingSelectionMode),
			field("restart_required", restartRequired))
		if restartRequired && s.pm.TunRunning() {
			started := time.Now()
			s.pm.event(serviceLogInfo, "manager", "settings.apply_start",
				"перезапуск VPN для применения настроек",
				field("bypass_domains", len(bypassDomainsFor(&cfgSnap))))
			_ = s.pm.Stop()
			if err := s.startManagedVPN(&cfgSnap); err != nil {
				s.pm.event(serviceLogError, "manager", "settings.apply_failed",
					"настройки сохранены, но VPN не запустился",
					field("error", err),
					field("duration_ms", time.Since(started).Milliseconds()))
				writeError(w, http.StatusInternalServerError, "settings saved, but VPN restart failed: "+err.Error())
				return
			}
			s.pm.event(serviceLogInfo, "manager", "settings.applied",
				"настройки применены к работающему VPN",
				field("duration_ms", time.Since(started).Milliseconds()))
		}
		writeJSON(w, snap)
	default:
		writeError(w, http.StatusMethodNotAllowed, "GET or PATCH only")
	}
}

func (s *apiServer) handleSettingsDefaults(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	writeJSON(w, defaultSettings())
}

type bypassStatus struct {
	Count           int        `json:"count"`
	EmbeddedCount   int        `json:"embedded_count"`
	CacheCount      int        `json:"cache_count"`
	CustomCount     int        `json:"custom_count"`
	EffectiveCount  int        `json:"effective_count"`
	Enabled         bool       `json:"enabled"`
	Applied         bool       `json:"applied"`
	UpdatedAt       *time.Time `json:"updated_at,omitempty"`
	Source          string     `json:"source"`
	SourceType      string     `json:"source_type"`
	Cached          bool       `json:"cached"`
	Restarted       bool       `json:"restarted,omitempty"`
	LastOperationID string     `json:"last_operation_id,omitempty"`
	LastAttemptAt   *time.Time `json:"last_attempt_at,omitempty"`
	LastSuccessAt   *time.Time `json:"last_success_at,omitempty"`
	LastTransport   string     `json:"last_transport,omitempty"`
	LastDurationMS  int64      `json:"last_duration_ms,omitempty"`
	LastError       string     `json:"last_error,omitempty"`
}

type bypassDiagnostics struct {
	OperationID string
	AttemptAt   time.Time
	SuccessAt   time.Time
	Transport   string
	DurationMS  int64
	Error       string
}

func (s *apiServer) bypassStatusLocked() bypassStatus {
	embeddedCount := len(parseDomainList(bypassRussiaWhitelist))
	cacheCount := len(s.cfg.BypassCache.Domains)
	effectiveCount := len(bypassDomainsFor(s.cfg))
	source := "встроенный в пакет"
	sourceType := "embedded"
	updatedAt := time.Time{}
	if cacheCount > 0 {
		source = s.cfg.BypassCache.Source
		sourceType = "cache"
		updatedAt = s.cfg.BypassCache.UpdatedAt
	}
	status := bypassStatus{
		Count:           embeddedCount,
		EmbeddedCount:   embeddedCount,
		CacheCount:      cacheCount,
		CustomCount:     len(s.cfg.Settings.BypassDomains),
		EffectiveCount:  effectiveCount,
		Enabled:         s.cfg.Settings.BypassRouteRussia,
		Applied:         s.pm.TunRunning(),
		Source:          source,
		SourceType:      sourceType,
		Cached:          cacheCount > 0,
		LastOperationID: s.bypassDiag.OperationID,
		LastTransport:   s.bypassDiag.Transport,
		LastDurationMS:  s.bypassDiag.DurationMS,
		LastError:       s.bypassDiag.Error,
	}
	if !updatedAt.IsZero() {
		status.UpdatedAt = &updatedAt
	}
	if !s.bypassDiag.AttemptAt.IsZero() {
		attemptAt := s.bypassDiag.AttemptAt
		status.LastAttemptAt = &attemptAt
	}
	if !s.bypassDiag.SuccessAt.IsZero() {
		successAt := s.bypassDiag.SuccessAt
		status.LastSuccessAt = &successAt
	}
	if len(s.cfg.BypassCache.Domains) > 0 {
		status.Count = cacheCount
	}
	return status
}

func (s *apiServer) handleBypass(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.mu.RLock()
		status := s.bypassStatusLocked()
		s.mu.RUnlock()
		writeJSON(w, status)
	case http.MethodPost:
		st := s.settingsSnapshot()
		opID := s.pm.nextOperationID("bypass")
		started := time.Now()
		timeout := st.SubscriptionFetchTimeout()
		s.mu.Lock()
		s.bypassDiag = bypassDiagnostics{
			OperationID: opID,
			AttemptAt:   started,
			Transport:   "wan",
		}
		currentCacheCount := len(s.cfg.BypassCache.Domains)
		s.mu.Unlock()
		s.pm.event(serviceLogInfo, "bypass", "refresh.start",
			"обновление RU-списка начато",
			field("op_id", opID),
			field("source", bypassWhitelistURL),
			field("timeout_ms", timeout.Milliseconds()),
			field("current_cache_domains", currentCacheCount))

		attemptStarted := time.Now()
		domains, err := fetchBypassWhitelist(directBypassHTTPClient(st.SubscriptionFetchTimeout()))
		transport := "wan"
		if err == nil {
			s.pm.event(serviceLogDebug, "bypass", "fetch.succeeded",
				"список загружен напрямую",
				field("op_id", opID),
				field("transport", transport),
				field("domains", len(domains)),
				field("duration_ms", time.Since(attemptStarted).Milliseconds()))
		}

		// If the normal router path cannot reach GitHub, use the configured
		// active VLESS node explicitly even when the main tunnel is stopped.
		if err != nil {
			s.pm.event(serviceLogWarn, "bypass", "fetch.failed",
				"прямая загрузка не удалась, проверяю возможность загрузки через VLESS",
				field("op_id", opID),
				field("transport", "wan"),
				field("duration_ms", time.Since(attemptStarted).Milliseconds()),
				field("error", err))
			s.mu.RLock()
			active := s.cfg.activeServer()
			var activeCopy *VLESSServer
			if active != nil {
				copy := *active
				activeCopy = &copy
			}
			s.mu.RUnlock()
			if activeCopy != nil {
				transport = "vless"
				attemptStarted = time.Now()
				s.pm.event(serviceLogInfo, "bypass", "fetch.retry",
					"повторная загрузка через активный VLESS-сервер",
					field("op_id", opID),
					field("transport", transport),
					field("server", activeCopy.Name),
					field("address", activeCopy.Address))
				client, box, proxyErr := httpClientViaVLESS(activeCopy, st.SubscriptionFetchTimeout(), st.PingStartupSleep(), s.pm.logs)
				if proxyErr == nil {
					domains, err = fetchBypassWhitelist(client)
					_ = box.Close()
					if err == nil {
						s.pm.event(serviceLogDebug, "bypass", "fetch.succeeded",
							"список загружен через VLESS",
							field("op_id", opID),
							field("transport", transport),
							field("server", activeCopy.Name),
							field("domains", len(domains)),
							field("duration_ms", time.Since(attemptStarted).Milliseconds()))
					}
				} else {
					s.pm.event(serviceLogWarn, "bypass", "proxy.start_failed",
						"не удалось подготовить временный VLESS-клиент",
						field("op_id", opID),
						field("server", activeCopy.Name),
						field("error", proxyErr))
				}
			} else {
				s.pm.event(serviceLogWarn, "bypass", "fetch.no_fallback",
					"активный VLESS-сервер не настроен, повторная загрузка невозможна",
					field("op_id", opID))
			}
		}
		if err != nil {
			duration := time.Since(started).Milliseconds()
			s.mu.Lock()
			s.bypassDiag.Transport = transport
			s.bypassDiag.DurationMS = duration
			s.bypassDiag.Error = err.Error()
			s.mu.Unlock()
			s.pm.event(serviceLogError, "bypass", "refresh.failed",
				"RU-список не обновлён, продолжает использоваться предыдущая версия",
				field("op_id", opID),
				field("transport", transport),
				field("duration_ms", duration),
				field("error", err))
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}

		now := time.Now()
		s.mu.Lock()
		s.cfg.BypassCache = BypassCache{
			Domains: domains, UpdatedAt: now, Source: bypassWhitelistURL,
		}
		if err := saveConfig(s.cfgPath, s.cfg); err != nil {
			s.bypassDiag.Transport = transport
			s.bypassDiag.DurationMS = time.Since(started).Milliseconds()
			s.bypassDiag.Error = err.Error()
			s.mu.Unlock()
			s.pm.event(serviceLogError, "bypass", "persist.failed",
				"список загружен, но не сохранён",
				field("op_id", opID),
				field("domains", len(domains)),
				field("error", err))
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		cfgSnap := *s.cfg
		s.mu.Unlock()

		if s.pm.TunRunning() {
			restartStarted := time.Now()
			s.pm.event(serviceLogInfo, "bypass", "apply.start",
				"перезапуск VPN для применения нового списка",
				field("op_id", opID),
				field("domains", len(domains)))
			_ = s.pm.Stop()
			if err := s.startManagedVPN(&cfgSnap); err != nil {
				s.mu.Lock()
				s.bypassDiag.Transport = transport
				s.bypassDiag.DurationMS = time.Since(started).Milliseconds()
				s.bypassDiag.Error = "VPN restart failed: " + err.Error()
				s.mu.Unlock()
				s.pm.event(serviceLogError, "bypass", "apply.failed",
					"список сохранён, но VPN не запустился",
					field("op_id", opID),
					field("error", err),
					field("duration_ms", time.Since(restartStarted).Milliseconds()))
				writeError(w, http.StatusInternalServerError, "list saved, but VPN restart failed: "+err.Error())
				return
			}
			s.pm.event(serviceLogInfo, "bypass", "apply.succeeded",
				"новый список применён к работающему VPN",
				field("op_id", opID),
				field("duration_ms", time.Since(restartStarted).Milliseconds()))
		}

		duration := time.Since(started).Milliseconds()
		s.mu.Lock()
		s.bypassDiag.Transport = transport
		s.bypassDiag.DurationMS = duration
		s.bypassDiag.SuccessAt = time.Now()
		s.bypassDiag.Error = ""
		status := s.bypassStatusLocked()
		status.Restarted = cfgSnap.ActiveServer != "" && s.pm.TunRunning()
		s.mu.Unlock()
		s.pm.event(serviceLogInfo, "bypass", "refresh.succeeded",
			"RU-список обновлён и готов к использованию",
			field("op_id", opID),
			field("transport", transport),
			field("domains", len(domains)),
			field("effective_domains", status.EffectiveCount),
			field("duration_ms", duration),
			field("vpn_running", status.Applied))
		writeJSON(w, status)
	default:
		writeError(w, http.StatusMethodNotAllowed, "GET or POST only")
	}
}

// handleFailover:
//
//	GET  → current state (enabled, last reason, probe results, pending)
//	POST {"enabled":bool} → automatic VPN on/off based on operator access
//	POST {"tunnel_failover_enabled":bool} → health-check and replace a bad tunnel
func (s *apiServer) handleFailover(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, s.failover.State())
	case http.MethodPost:
		var req struct {
			Enabled               *bool `json:"enabled"`
			TunnelFailoverEnabled *bool `json:"tunnel_failover_enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if req.Enabled == nil && req.TunnelFailoverEnabled == nil {
			writeError(w, http.StatusBadRequest, "enabled or tunnel_failover_enabled is required")
			return
		}
		s.mu.Lock()
		previousOuter := s.cfg.AutoFailover
		previousTunnel := s.cfg.AutoTunnelFailover
		if req.Enabled != nil {
			s.cfg.AutoFailover = *req.Enabled
		}
		if req.TunnelFailoverEnabled != nil {
			s.cfg.AutoTunnelFailover = *req.TunnelFailoverEnabled
		}
		if err := s.persistConfigLocked(); err != nil {
			s.cfg.AutoFailover = previousOuter
			s.cfg.AutoTunnelFailover = previousTunnel
			s.mu.Unlock()
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		outerEnabled := s.cfg.AutoFailover
		tunnelEnabled := s.cfg.AutoTunnelFailover
		s.mu.Unlock()
		s.failover.SetEnabled(outerEnabled)
		s.failover.SetTunnelFailoverEnabled(tunnelEnabled)
		s.pm.event(serviceLogInfo, "failover", "controller.toggled",
			"настройки автоматического управления изменены",
			field("operator_control_enabled", outerEnabled),
			field("tunnel_failover_enabled", tunnelEnabled))
		writeJSON(w, s.failover.State())
	default:
		writeError(w, http.StatusMethodNotAllowed, "GET or POST only")
	}
}

func (s *apiServer) handleInternetCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	writeJSON(w, s.checkInternet("manual"))
}

func (s *apiServer) checkInternet(trigger string) InternetStatus {
	started := time.Now()
	status := s.health.Check()
	level := serviceLogDebug
	if !status.OK {
		level = serviceLogWarn
	}
	s.pm.event(level, "network", "wan_check.completed",
		"проверка прямого доступа в интернет завершена",
		field("trigger", trigger),
		field("ok", status.OK),
		field("reachable", status.Reachable),
		field("total", status.Total),
		field("latency_ms", status.LatencyMS),
		field("duration_ms", time.Since(started).Milliseconds()))
	for _, probe := range status.Probes {
		s.pm.event(serviceLogTrace, "network", "wan_check.probe",
			"результат контрольного запроса",
			field("trigger", trigger),
			field("url", probe.URL),
			field("ok", probe.OK),
			field("latency_ms", probe.LatencyMS),
			field("error", probe.Error))
	}
	return status
}

func (s *apiServer) handleVersion(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	writeJSON(w, buildInfo())
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// The caller holds s.mu while these methods serialize the corresponding
// in-memory state. Persistence failures are always visible in the service log,
// including background operations that have no HTTP response to report them.
func (s *apiServer) persistConfigLocked() error {
	if err := saveConfig(s.cfgPath, s.cfg); err != nil {
		s.pm.event(serviceLogError, "manager", "config.persist_failed",
			"не удалось сохранить конфигурацию",
			field("path", s.cfgPath),
			field("error", err))
		return err
	}
	return nil
}

func (s *apiServer) persistSubscriptionsLocked() error {
	if err := saveSubscriptions(s.subPath, s.subs); err != nil {
		s.pm.event(serviceLogError, "subscription", "persist.failed",
			"не удалось сохранить подписки",
			field("path", s.subPath),
			field("error", err))
		return err
	}
	return nil
}

// --- Status ---

func (s *apiServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	status := s.pm.Status()

	s.mu.RLock()
	srv := s.cfg.activeServer()
	serverName := ""
	serverID := ""
	serverDetails := map[string]any{}
	if srv != nil {
		serverName = srv.Name
		serverID = srv.ID
		serverDetails = map[string]any{
			"address":  srv.Address,
			"port":     srv.Port,
			"network":  srv.Network,
			"security": srv.Security,
			"sni":      srv.SNI,
			"flow":     srv.Flow,
			"manual":   srv.Manual,
		}
		for _, sub := range s.subs {
			for _, subServer := range sub.Servers {
				if sameCatalogServer(*srv, subServer) {
					serverDetails["subscription"] = sub.Name
					break
				}
			}
			if _, found := serverDetails["subscription"]; found {
				break
			}
		}
		if _, found := serverDetails["subscription"]; !found && status.Running && !srv.Manual {
			serverDetails["subscription"] = "Удалённая подписка"
		}
	}
	subInfo := s.activeSubUserInfoLocked()
	bypassCount := len(bypassDomainsFor(s.cfg))
	s.mu.RUnlock()

	s.pingMu.Lock()
	pingProg := s.pingProgress
	s.pingMu.Unlock()
	activeLatency := int64(-1)
	if result, ok := s.pingCache.Get(serverID); ok {
		activeLatency = result.LatencyMS
	} else {
		s.pingMu.Lock()
		for _, result := range s.lastPing {
			if result.ServerID == serverID {
				activeLatency = result.LatencyMS
				break
			}
		}
		s.pingMu.Unlock()
	}
	failoverState := s.failover.State()
	if activeLatency < 0 && status.Running && failoverState.VPNHealthOK &&
		!failoverState.VPNHealthCheck.IsZero() && failoverState.VPNHealthLatencyMS >= 0 {
		activeLatency = failoverState.VPNHealthLatencyMS
	}

	writeJSON(w, map[string]any{
		"running":                  status.Running,
		"pid":                      status.PID,
		"uptime_sec":               status.UptimeSec,
		"uptime":                   formatUptime(status.UptimeSec),
		"error":                    status.Error,
		"active_server":            serverName,
		"active_server_details":    serverDetails,
		"active_server_latency_ms": activeLatency,
		"bypass_effective_count":   bypassCount,
		"tun_running":              s.pm.TunRunning(),
		"route_ready":              status.RouteReady,
		"internet":                 s.health.Status(),
		"sub_user_info":            subInfo,
		"failover":                 failoverState,
		"ping_progress":            pingProg,
	})
}

func sameCatalogServer(active, candidate VLESSServer) bool {
	if active.ID != "" && active.ID == candidate.ID {
		return true
	}
	return active.Name != "" &&
		active.Name == candidate.Name &&
		active.Address == candidate.Address &&
		active.Port == candidate.Port
}

// --- Config ---

func (s *apiServer) handleConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.mu.RLock()
		defer s.mu.RUnlock()
		writeJSON(w, s.cfg)
	case http.MethodPost:
		var newCfg Config
		if err := json.NewDecoder(r.Body).Decode(&newCfg); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if newCfg.Port == 0 {
			newCfg.Port = s.cfg.Port
		}
		if newCfg.Servers == nil {
			newCfg.Servers = s.cfg.Servers
		}
		newCfg.Settings.fillDefaults()
		if err := newCfg.Settings.validate(); err != nil {
			writeError(w, http.StatusBadRequest, "invalid settings: "+err.Error())
			return
		}
		migrateServerIDs(&newCfg)
		if err := saveConfig(s.cfgPath, &newCfg); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		s.cfg = &newCfg
		s.pm.SetServiceLogLevel(newCfg.Settings.ServiceLogLevel)
		s.failover.ReloadSettings()
		s.failover.SetEnabled(newCfg.AutoFailover)
		s.failover.SetTunnelFailoverEnabled(newCfg.AutoTunnelFailover)
		writeJSON(w, s.cfg)
	default:
		writeError(w, http.StatusMethodNotAllowed, "GET or POST only")
	}
}

// --- Process control ---

// autoSelectBest always performs a fresh explicit selection. Automatic start
// and failover use prepareServerForStart/chooseAlternativeServer, which may
// reuse a fresh cache according to settings.
func (s *apiServer) autoSelectBest() string {
	server, result := s.findBestConfigured("", false, false)
	if server == nil || result == nil {
		s.mu.Lock()
		s.cfg.ActiveServer = ""
		_ = s.persistConfigLocked()
		s.mu.Unlock()
		s.pm.event(serviceLogWarn, "priority", "selection.failed",
			"ни одна включённая подписка не дала рабочего доступа")
		return ""
	}
	s.commitSelectedServer(preferGroupMember(*server, result.SelectedMemberID))
	s.pm.event(serviceLogInfo, "priority", "selection.succeeded",
		"автоматически выбран сервер",
		field("server", result.ServerName),
		field("latency_ms", result.LatencyMS))
	return result.ServerName
}

func (s *apiServer) handleStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	// User explicitly hit "Start" — wipe any failover backoff so the next
	// outer/health tick doesn't ignore them after we finish.
	if s.failover != nil {
		s.failover.ResetBackoff()
	}
	if _, err := s.prepareServerForStart(); err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}

	s.mu.RLock()
	cfgSnap := *s.cfg
	s.mu.RUnlock()

	if err := s.startManagedVPN(&cfgSnap); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, map[string]string{"status": "started"})
}

func (s *apiServer) handleStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	s.cancelPingRun("vpn_stop")
	if err := s.pm.Stop(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.mu.Lock()
	pruned := pruneStaleServers(s.cfg, s.subs)
	if pruned > 0 {
		_ = s.persistConfigLocked()
	}
	s.mu.Unlock()
	if pruned > 0 {
		s.pm.event(serviceLogInfo, "subscription", "detached_active.cleaned",
			"после остановки VPN удалён профиль, которого больше нет в подписках",
			field("servers", pruned))
	}
	writeJSON(w, map[string]string{"status": "stopped"})
}

func (s *apiServer) handleRestart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	s.cancelPingRun("vpn_restart")
	if err := s.pm.Stop(); err != nil {
		writeError(w, http.StatusInternalServerError, "stop before restart: "+err.Error())
		return
	}
	time.Sleep(500 * time.Millisecond)

	s.mu.RLock()
	cfgSnap := *s.cfg
	s.mu.RUnlock()

	if err := s.startManagedVPN(&cfgSnap); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, map[string]string{"status": "restarted"})
}

// --- Logs ---

func (s *apiServer) handleLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	sinceStr := r.URL.Query().Get("since")
	since, _ := strconv.Atoi(sinceStr)
	lines, seq := s.pm.logs.Lines(since)
	entries, _ := s.pm.logs.Entries(since)
	writeJSON(w, map[string]any{"lines": lines, "entries": entries, "seq": seq})
}

// --- Manual Servers ---

func (s *apiServer) handleServers(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.mu.RLock()
		defer s.mu.RUnlock()
		writeJSON(w, s.cfg.Servers)
	case http.MethodPost:
		var srv VLESSServer
		if err := json.NewDecoder(r.Body).Decode(&srv); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if srv.Address == "" || srv.UUID == "" {
			writeError(w, http.StatusBadRequest, "address and uuid required")
			return
		}
		if srv.Name == "" {
			srv.Name = srv.Address
		}
		srv.Network = normalizeVLESSNetwork(srv.Network)
		if !isSupportedServer(&srv) {
			writeError(w, http.StatusBadRequest, "transport "+srv.Network+" is not supported by sing-box "+BundledSingBox)
			return
		}
		if srv.Fingerprint == "" {
			srv.Fingerprint = "chrome"
		}
		srv.Manual = true
		srv.ID = serverFingerprint(srv)
		s.mu.Lock()
		defer s.mu.Unlock()
		s.cfg.Servers = append(s.cfg.Servers, srv)
		if err := s.persistConfigLocked(); err != nil {
			s.cfg.Servers = s.cfg.Servers[:len(s.cfg.Servers)-1]
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, srv)
	default:
		writeError(w, http.StatusMethodNotAllowed, "GET or POST only")
	}
}

func (s *apiServer) handleServerByID(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/servers/")
	parts := strings.SplitN(path, "/", 2)
	id := parts[0]
	action := ""
	if len(parts) == 2 {
		action = parts[1]
	}

	s.mu.Lock()
	idx := -1
	for i, srv := range s.cfg.Servers {
		if srv.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		s.mu.Unlock()
		writeError(w, http.StatusNotFound, "server not found")
		return
	}

	switch {
	case r.Method == http.MethodGet && action == "":
		out := s.cfg.Servers[idx]
		s.mu.Unlock()
		writeJSON(w, out)
	case r.Method == http.MethodPut && action == "":
		var srv VLESSServer
		if err := json.NewDecoder(r.Body).Decode(&srv); err != nil {
			s.mu.Unlock()
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		srv.ID = id
		srv.Manual = true
		srv.Network = normalizeVLESSNetwork(srv.Network)
		if !isSupportedServer(&srv) {
			s.mu.Unlock()
			writeError(w, http.StatusBadRequest, "transport "+srv.Network+" is not supported by sing-box "+BundledSingBox)
			return
		}
		s.cfg.Servers[idx] = srv
		_ = s.persistConfigLocked()
		s.mu.Unlock()
		writeJSON(w, srv)
	case r.Method == http.MethodDelete && action == "":
		s.cfg.Servers = append(s.cfg.Servers[:idx], s.cfg.Servers[idx+1:]...)
		if s.cfg.ActiveServer == id {
			s.cfg.ActiveServer = ""
		}
		_ = s.persistConfigLocked()
		s.mu.Unlock()
		writeJSON(w, map[string]string{"status": "deleted"})
	case r.Method == http.MethodPost && action == "connect":
		if !isSupportedServer(&s.cfg.Servers[idx]) {
			s.mu.Unlock()
			writeError(w, http.StatusConflict, "server transport is not supported")
			return
		}
		name := s.cfg.Servers[idx].Name
		s.mu.Unlock()
		s.connectServer(w, id, name)
	default:
		s.mu.Unlock()
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// --- Subscriptions ---

func (s *apiServer) handleSubscriptions(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.mu.RLock()
		defer s.mu.RUnlock()
		if s.subs == nil {
			writeJSON(w, []struct{}{})
			return
		}
		writeJSON(w, s.subs)
	case http.MethodPost:
		var req struct {
			URL  string `json:"url"`
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if req.URL == "" {
			writeError(w, http.StatusBadRequest, "url required")
			return
		}
		// Slow network operation — DO NOT hold the lock.
		s.pm.log(serviceLogInfo, "[subscription] добавление: начинаю загрузку")
		sub, err := s.fetchSubscriptionURL(req.URL)
		if err != nil {
			s.pm.log(serviceLogWarn, "[subscription] добавление не удалось: %v", err)
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
		if req.Name != "" {
			sub.Name = req.Name
		}
		s.mu.Lock()
		s.subs = append(s.subs, sub)
		_ = s.persistSubscriptionsLocked()
		s.mu.Unlock()
		s.pm.log(serviceLogInfo, "[subscription] добавлено %q: %d серверов, %d исключено", sub.Name, len(sub.Servers), sub.ExcludedServers)
		writeJSON(w, sub)
	default:
		writeError(w, http.StatusMethodNotAllowed, "GET or POST only")
	}
}

func (s *apiServer) handleSubscriptionByID(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/subscriptions/")
	parts := strings.SplitN(path, "/", 2)
	id := parts[0]
	action := ""
	if len(parts) == 2 {
		action = parts[1]
	}

	// Find index + snapshot URL/name under lock so the slow refresh fetch
	// runs unlocked.
	s.mu.RLock()
	idx := -1
	for i, sub := range s.subs {
		if sub.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		s.mu.RUnlock()
		writeError(w, http.StatusNotFound, "subscription not found")
		return
	}

	switch {
	case r.Method == http.MethodDelete && action == "":
		s.mu.RUnlock()
		vpnRunning := s.pm.Status().Running
		s.mu.Lock()
		// Re-find under write lock (idx may have shifted).
		idx = -1
		for i, sub := range s.subs {
			if sub.ID == id {
				idx = i
				break
			}
		}
		if idx < 0 {
			s.mu.Unlock()
			writeError(w, http.StatusNotFound, "subscription not found")
			return
		}
		s.subs = append(s.subs[:idx], s.subs[idx+1:]...)
		activeBefore := s.cfg.ActiveServer
		pruned := 0
		if vpnRunning {
			pruned = pruneStaleServersPreservingActive(s.cfg, s.subs)
		} else {
			pruned = pruneStaleServers(s.cfg, s.subs)
		}
		activeRemoved := activeBefore != "" && s.cfg.ActiveServer == ""
		activeDetached := vpnRunning && activeBefore != "" &&
			s.cfg.ActiveServer == activeBefore && !s.serverAvailableLocked(activeBefore)
		_ = s.persistSubscriptionsLocked()
		if pruned > 0 || activeRemoved || activeDetached {
			_ = s.persistConfigLocked()
		}
		keep := make(map[string]bool)
		for _, server := range s.allServersLocked() {
			keep[server.ID] = true
		}
		if activeDetached {
			keep[activeBefore] = true
		}
		s.mu.Unlock()

		s.pingCache.Prune(keep)
		if activeDetached {
			s.pm.event(serviceLogInfo, "subscription", "active.detached",
				"подписка удалена; текущий VPN продолжает работу до остановки или переключения",
				field("server_id", activeBefore))
		}
		if pruned > 0 {
			s.pm.log(serviceLogInfo, "[subscription] удалено сохранённых серверов: %d", pruned)
		}
		writeJSON(w, map[string]string{"status": "deleted"})

	case r.Method == http.MethodPost && action == "move":
		var req struct {
			Direction int `json:"direction"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || (req.Direction != -1 && req.Direction != 1) {
			s.mu.RUnlock()
			writeError(w, http.StatusBadRequest, "direction must be -1 or 1")
			return
		}
		s.mu.RUnlock()
		s.mu.Lock()
		idx = -1
		for i, sub := range s.subs {
			if sub.ID == id {
				idx = i
				break
			}
		}
		if idx < 0 {
			s.mu.Unlock()
			writeError(w, http.StatusNotFound, "subscription not found")
			return
		}
		target := idx + req.Direction
		if target < 0 || target >= len(s.subs) {
			response := *s.subs[idx]
			s.mu.Unlock()
			writeJSON(w, &response)
			return
		}
		s.subs[idx], s.subs[target] = s.subs[target], s.subs[idx]
		_ = s.persistSubscriptionsLocked()
		response := *s.subs[target]
		s.mu.Unlock()
		s.pm.log(serviceLogInfo, "[priority] подписка %q перемещена на позицию %d", response.Name, target+1)
		writeJSON(w, &response)

	case r.Method == http.MethodPost && action == "refresh":
		if s.subs[idx].Disabled {
			s.mu.RUnlock()
			writeError(w, http.StatusConflict, "subscription is disabled")
			return
		}
		current := *s.subs[idx]
		current.Servers = append([]VLESSServer(nil), s.subs[idx].Servers...)
		s.mu.RUnlock()

		s.pm.log(serviceLogInfo, "[subscription] обновление %q: начинаю загрузку", current.Name)
		sub, err := s.fetchSubscriptionURL(current.URL)

		s.mu.Lock()
		// Re-find under write lock.
		idx = -1
		for i, sub := range s.subs {
			if sub.ID == id {
				idx = i
				break
			}
		}
		if idx < 0 {
			s.mu.Unlock()
			writeError(w, http.StatusNotFound, "subscription removed during refresh")
			return
		}
		if err != nil {
			s.subs[idx].Error = err.Error()
			_ = s.persistSubscriptionsLocked()
			s.mu.Unlock()
			s.pm.log(serviceLogWarn, "[subscription] обновление %q не удалось: %v", current.Name, err)
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
		sub.ID = id
		preserveSubscriptionDisplayName(sub, &current)
		preserveSubscriptionOptions(sub, &current)
		migratedPings := s.pingCache.MigrateEquivalent(current.Servers, sub.Servers)
		s.subs[idx] = sub
		_ = s.persistSubscriptionsLocked()
		// Sync cached profiles while preserving the currently running
		// connection. Catalog refreshes do not control tunnel lifetime.
		activeBefore := s.cfg.ActiveServer
		resynced := resyncServersFromSubs(s.cfg, s.subs)
		pruned := pruneStaleServersPreservingActive(s.cfg, s.subs)
		activeRemoved := activeBefore != "" && s.cfg.ActiveServer == ""
		if resynced > 0 || pruned > 0 || activeRemoved {
			_ = s.persistConfigLocked()
		}
		s.mu.Unlock()
		// Refreshing a catalog must never stop a working tunnel. Tunnel
		// replacement is exclusively driven by the sustained health-check.
		if migratedPings > 0 {
			s.pm.event(serviceLogInfo, "subscription", "ping_history.migrated",
				"история проверок перенесена на обновлённые записи серверов",
				field("subscription", sub.Name),
				field("servers", migratedPings))
		}
		s.pm.log(serviceLogInfo, "[subscription] обновлено %q: %d серверов, %d исключено", sub.Name, len(sub.Servers), sub.ExcludedServers)
		writeJSON(w, sub)

	case r.Method == http.MethodPost && action == "connect":
		if s.subs[idx].Disabled {
			s.mu.RUnlock()
			writeError(w, http.StatusConflict, "subscription is disabled")
			return
		}
		var req struct {
			Index int `json:"index"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			s.mu.RUnlock()
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		srvs := s.subs[idx].Servers
		if req.Index < 0 || req.Index >= len(srvs) {
			s.mu.RUnlock()
			writeError(w, http.StatusBadRequest, "invalid index")
			return
		}
		srv := srvs[req.Index]
		if s.subs[idx].serverDisabled(srv.ID) {
			s.mu.RUnlock()
			writeError(w, http.StatusConflict, "server is disabled")
			return
		}
		s.mu.RUnlock()

		if len(srv.Members) > 0 {
			results := s.pingServers([]VLESSServer{srv})
			if len(results) == 0 || results[0].LatencyMS < 0 {
				writeError(w, http.StatusBadGateway, "ни один узел автопрофиля не отвечает")
				return
			}
			srv = preferGroupMember(srv, results[0].SelectedMemberID)
		}
		s.mu.Lock()
		s.upsertServerLocked(srv)
		s.mu.Unlock()
		s.connectServer(w, srv.ID, srv.Name)

	case r.Method == http.MethodPost && action == "ping":
		if s.subs[idx].Disabled {
			s.mu.RUnlock()
			writeError(w, http.StatusConflict, "subscription is disabled")
			return
		}
		// Snapshot just this subscription's server list, drop the lock, then
		// run the ping cycle. `runPingAll` updates pingCache so the UI picks
		// up new latencies on the next /api/ping GET.
		srvs := enabledSubscriptionServers(s.subs[idx])
		s.mu.RUnlock()
		if len(srvs) == 0 {
			writeJSON(w, []PingResult{})
			return
		}
		results := s.pingServers(srvs)
		writeJSON(w, results)

	case r.Method == http.MethodPatch && action == "":
		var req struct {
			Name     *string `json:"name"`
			URL      *string `json:"url"`
			Disabled *bool   `json:"disabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			s.mu.RUnlock()
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if req.Name == nil && req.URL == nil && req.Disabled == nil {
			s.mu.RUnlock()
			writeError(w, http.StatusBadRequest, "name, url or disabled required")
			return
		}
		name := ""
		if req.Name != nil {
			name = strings.TrimSpace(*req.Name)
			if name == "" {
				s.mu.RUnlock()
				writeError(w, http.StatusBadRequest, "name required")
				return
			}
		}
		normalizedURL := ""
		newID := id
		if req.URL != nil {
			normalizedURL = normalizeSubscriptionURL(*req.URL)
			parsed, err := url.Parse(normalizedURL)
			if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
				s.mu.RUnlock()
				writeError(w, http.StatusBadRequest, "valid http/https subscription URL required")
				return
			}
			newID = subscriptionID(normalizedURL)
		}
		s.mu.RUnlock()
		s.mu.Lock()
		idx = -1
		for i, sub := range s.subs {
			if sub.ID == id {
				idx = i
				break
			}
		}
		if idx < 0 {
			s.mu.Unlock()
			writeError(w, http.StatusNotFound, "subscription not found")
			return
		}
		if newID != id {
			for i, sub := range s.subs {
				if i != idx && sub.ID == newID {
					s.mu.Unlock()
					writeError(w, http.StatusConflict, "subscription with this URL already exists")
					return
				}
			}
			s.subs[idx].URL = normalizedURL
			s.subs[idx].ID = newID
			s.subs[idx].Error = ""
		}
		if req.Name != nil {
			s.subs[idx].Name = name
		}
		stoppedActive := false
		if req.Disabled != nil {
			s.subs[idx].Disabled = *req.Disabled
			if *req.Disabled {
				for _, server := range s.subs[idx].Servers {
					if server.ID == s.cfg.ActiveServer && !s.serverAvailableLocked(server.ID) {
						s.cfg.ActiveServer = ""
						_ = s.persistConfigLocked()
						stoppedActive = true
						break
					}
				}
			}
		}
		_ = s.persistSubscriptionsLocked()
		response := *s.subs[idx]
		s.mu.Unlock()
		if stoppedActive {
			_ = s.pm.Stop()
			s.pm.log(serviceLogInfo, "[priority] активная подписка %q выключена; VPN остановлен", response.Name)
		}
		writeJSON(w, &response)

	case r.Method == http.MethodPatch && strings.HasPrefix(action, "servers/"):
		serverID := strings.TrimPrefix(action, "servers/")
		var req struct {
			Disabled *bool `json:"disabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Disabled == nil {
			s.mu.RUnlock()
			writeError(w, http.StatusBadRequest, "disabled boolean required")
			return
		}
		found := false
		for _, srv := range s.subs[idx].Servers {
			if srv.ID == serverID {
				found = true
				break
			}
		}
		if !found {
			s.mu.RUnlock()
			writeError(w, http.StatusNotFound, "server not found in subscription")
			return
		}
		s.mu.RUnlock()

		s.mu.Lock()
		idx = -1
		for i, sub := range s.subs {
			if sub.ID == id {
				idx = i
				break
			}
		}
		if idx < 0 {
			s.mu.Unlock()
			writeError(w, http.StatusNotFound, "subscription not found")
			return
		}
		s.subs[idx].setServerDisabled(serverID, *req.Disabled)
		stoppedActive := *req.Disabled && s.cfg.ActiveServer == serverID && !s.serverAvailableLocked(serverID)
		if stoppedActive {
			s.cfg.ActiveServer = ""
			_ = s.persistConfigLocked()
		}
		_ = s.persistSubscriptionsLocked()
		response := *s.subs[idx]
		s.mu.Unlock()
		if stoppedActive {
			_ = s.pm.Stop()
			s.pm.log(serviceLogWarn, "[manager] active server disabled; VPN stopped")
		}
		writeJSON(w, &response)

	default:
		s.mu.RUnlock()
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// --- Ping ---

// handlePing:
//
//	GET  /api/ping  — return cached last results (loaded from disk).
//	POST /api/ping  — run a fresh test, cache & return.
func (s *apiServer) handlePing(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, s.pingCache.All())
	case http.MethodPost:
		s.mu.RLock()
		all := s.allServersLocked()
		s.mu.RUnlock()
		if len(all) == 0 {
			writeJSON(w, []PingResult{})
			return
		}
		// Slow op (sequential temp sing-box per server) — no lock held.
		results := s.runPingAll(all)
		writeJSON(w, results)
	default:
		writeError(w, http.StatusMethodNotAllowed, "GET or POST only")
	}
}

func (s *apiServer) handlePingAutoSelect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	name := s.autoSelectBest()
	if name == "" {
		writeError(w, http.StatusServiceUnavailable, "no enabled subscription has working internet")
		return
	}
	s.mu.RLock()
	id := s.cfg.ActiveServer
	s.mu.RUnlock()
	s.connectServer(w, id, name)
}

// --- Traffic ---

// handleTraffic returns user-facing download/upload counters. The LAN bridge
// sees client uploads as RX and client downloads as TX, so its raw kernel
// counters must be reversed before they are shown in the UI.
func (s *apiServer) handleTraffic(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	iface := chooseLanIface()
	rx, _ := readUint64File("/sys/class/net/" + iface + "/statistics/rx_bytes")
	tx, _ := readUint64File("/sys/class/net/" + iface + "/statistics/tx_bytes")
	download, upload := trafficDirections(iface, rx, tx)
	outbound, vpnRunning := s.pm.TrafficSnapshot()
	writeJSON(w, map[string]any{
		"interface":      iface,
		"download_bytes": download,
		"upload_bytes":   upload,
		"rx_bytes":       rx,
		"tx_bytes":       tx,
		"vpn_running":    vpnRunning,
		"modes": map[string]any{
			"all": map[string]any{
				"download_bytes": download,
				"upload_bytes":   upload,
				"available":      true,
			},
			"vpn": map[string]any{
				"download_bytes": outbound.VPNDownload,
				"upload_bytes":   outbound.VPNUpload,
				"available":      vpnRunning,
			},
			"bypass": map[string]any{
				"download_bytes": outbound.BypassDownload,
				"upload_bytes":   outbound.BypassUpload,
				"available":      vpnRunning,
			},
		},
		"timestamp": time.Now().UnixMilli(),
	})
}

func trafficDirections(iface string, rx, tx uint64) (download, upload uint64) {
	if iface == "br0" || iface == "br-lan" {
		return tx, rx
	}
	return rx, tx
}

// --- Parse URI ---

func (s *apiServer) handleParseURI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var req struct {
		URI string `json:"uri"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	srv, err := parseVLESSURI(req.URI)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !isSupportedServer(srv) {
		writeError(w, http.StatusBadRequest, "transport "+srv.Network+" is not supported by sing-box "+BundledSingBox)
		return
	}
	writeJSON(w, srv)
}

// --- Helpers ---
//
// *Locked variants assume s.mu is held by the caller. Helpers without the
// suffix acquire the lock themselves.

// priorityServerGroupsLocked builds the effective selection order. Subscription
// slice order is the user-controlled priority; explicit manual servers are a
// final fallback group. Cached subscription copies in cfg.Servers are ignored.
// Caller must hold s.mu (R or W).
func (s *apiServer) priorityServerGroupsLocked() []priorityServerGroup {
	seen := make(map[string]bool)
	groups := make([]priorityServerGroup, 0, len(s.subs)+1)
	for _, sub := range s.subs {
		if sub.Disabled {
			continue
		}
		group := priorityServerGroup{ID: sub.ID, Name: sub.Name}
		for _, srv := range sub.Servers {
			if !sub.serverDisabled(srv.ID) && !seen[srv.ID] {
				seen[srv.ID] = true
				group.Servers = append(group.Servers, srv)
			}
		}
		if len(group.Servers) > 0 {
			groups = append(groups, group)
		}
	}

	manual := priorityServerGroup{ID: "__manual__", Name: "Ручные серверы"}
	for _, srv := range s.cfg.Servers {
		if srv.Manual && !seen[srv.ID] {
			seen[srv.ID] = true
			manual.Servers = append(manual.Servers, srv)
		}
	}
	if len(manual.Servers) > 0 {
		groups = append(groups, manual)
	}
	return groups
}

func (s *apiServer) serverAvailableLocked(id string) bool {
	for _, group := range s.priorityServerGroupsLocked() {
		for _, server := range group.Servers {
			if server.ID == id {
				return true
			}
		}
	}
	return false
}

func rotatePriorityGroups(groups []priorityServerGroup, activeID string) []priorityServerGroup {
	if len(groups) < 2 || activeID == "" {
		return groups
	}
	activeGroup := -1
	for i, group := range groups {
		for _, server := range group.Servers {
			if server.ID == activeID {
				activeGroup = i
				break
			}
		}
		if activeGroup >= 0 {
			break
		}
	}
	if activeGroup <= 0 {
		return groups
	}
	rotated := make([]priorityServerGroup, 0, len(groups))
	rotated = append(rotated, groups[activeGroup:]...)
	rotated = append(rotated, groups[:activeGroup]...)
	return rotated
}

func (s *apiServer) findBestPrioritized(excludeID string, rotateFromExcluded bool) (*VLESSServer, *PingResult) {
	return s.findBestPrioritizedWithCache(excludeID, rotateFromExcluded, false, 0)
}

func (s *apiServer) findBestPrioritizedWithCache(excludeID string, rotateFromExcluded, useCache bool, maxAge time.Duration) (*VLESSServer, *PingResult) {
	stopGeneration := s.pingStopGeneration()
	s.mu.RLock()
	groups := s.priorityServerGroupsLocked()
	s.mu.RUnlock()
	if rotateFromExcluded {
		groups = rotatePriorityGroups(groups, excludeID)
	}

	for _, group := range groups {
		if s.pingStoppedSince(stopGeneration) {
			return nil, nil
		}
		candidates := make([]VLESSServer, 0, len(group.Servers))
		for _, server := range group.Servers {
			if server.ID != excludeID {
				candidates = append(candidates, server)
			}
		}
		if len(candidates) == 0 {
			continue
		}
		opID := s.pm.nextOperationID("priority")
		s.pm.event(serviceLogInfo, "priority", "subscription.started",
			"начата проверка подписки по приоритету",
			field("op_id", opID),
			field("subscription", group.Name),
			field("servers", len(candidates)))
		results, cacheHit := s.freshCachedResults(candidates, maxAge)
		cacheComplete := cacheHit
		if useCache && !cacheHit {
			results, cacheHit = s.freshReachableCachedResults(candidates, maxAge)
		}
		if !useCache || !cacheHit {
			results = s.pingServersNamed(candidates, group.Name)
		} else {
			s.pm.event(serviceLogInfo, "priority", "subscription.cache_hit",
				"использованы свежие результаты проверки",
				field("op_id", opID),
				field("subscription", group.Name),
				field("cached_servers", len(results)),
				field("cache_complete", cacheComplete),
				field("cache_max_age_ms", maxAge.Milliseconds()))
		}
		if s.pingStoppedSince(stopGeneration) {
			return nil, nil
		}
		sortByLatency(results)
		best := fastestReachable(results)
		if best == nil {
			s.pm.event(serviceLogWarn, "priority", "subscription.failed",
				"подписка не дала рабочего доступа, проверяется следующая",
				field("op_id", opID),
				field("subscription", group.Name),
				field("servers", len(candidates)))
			continue
		}
		for i := range candidates {
			if candidates[i].ID == best.ServerID {
				server := candidates[i]
				result := *best
				s.pm.event(serviceLogInfo, "priority", "subscription.succeeded",
					"выбран самый быстрый сервер в приоритетной подписке",
					field("op_id", opID),
					field("subscription", group.Name),
					field("server", result.ServerName),
					field("latency_ms", result.LatencyMS))
				return &server, &result
			}
		}
	}
	return nil, nil
}

func (s *apiServer) findBestFastest(excludeID string, useCache bool, maxAge time.Duration) (*VLESSServer, *PingResult) {
	s.mu.RLock()
	servers := s.allServersLocked()
	s.mu.RUnlock()
	candidates := make([]VLESSServer, 0, len(servers))
	for _, server := range servers {
		if server.ID != excludeID {
			candidates = append(candidates, server)
		}
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	opID := s.pm.nextOperationID("fastest")
	results, cacheHit := s.freshCachedResults(candidates, maxAge)
	cacheComplete := cacheHit
	if useCache && !cacheHit {
		results, cacheHit = s.freshReachableCachedResults(candidates, maxAge)
	}
	if !useCache || !cacheHit {
		results = s.pingServersNamed(candidates, "Все подписки")
	} else {
		s.pm.event(serviceLogInfo, "priority", "global.cache_hit",
			"для глобального выбора использованы свежие результаты",
			field("op_id", opID),
			field("cached_servers", len(results)),
			field("cache_complete", cacheComplete),
			field("cache_max_age_ms", maxAge.Milliseconds()))
	}
	sortByLatency(results)
	best := fastestReachable(results)
	if best == nil {
		s.pm.event(serviceLogWarn, "priority", "global.failed",
			"ни один сервер не дал рабочего доступа",
			field("op_id", opID),
			field("servers", len(candidates)))
		return nil, nil
	}
	for i := range candidates {
		if candidates[i].ID == best.ServerID {
			server, result := candidates[i], *best
			s.pm.event(serviceLogInfo, "priority", "global.succeeded",
				"выбран самый быстрый сервер из всех подписок",
				field("op_id", opID),
				field("server", result.ServerName),
				field("latency_ms", result.LatencyMS))
			return &server, &result
		}
	}
	return nil, nil
}

func (s *apiServer) findBestConfigured(excludeID string, rotateFromExcluded, useCache bool) (*VLESSServer, *PingResult) {
	st := s.settingsSnapshot()
	if st.PingSelectionMode == "fastest" {
		return s.findBestFastest(excludeID, useCache, st.PingCacheMaxAge())
	}
	return s.findBestPrioritizedWithCache(excludeID, rotateFromExcluded, useCache, st.PingCacheMaxAge())
}

func (s *apiServer) prepareServerForStart() (string, error) {
	st := s.settingsSnapshot()
	stopGeneration := s.pingStopGeneration()
	// Clear the previous selection so status/UI cannot present a server from
	// another subscription as the pending connection. An explicit Start
	// always performs a fresh complete pass over the selected subscription
	// before choosing its fastest working server.
	s.mu.Lock()
	s.cfg.ActiveServer = ""
	_ = s.persistConfigLocked()
	s.mu.Unlock()

	server, result := s.findBestConfigured("", false, false)
	if s.pingStoppedSince(stopGeneration) {
		return "", fmt.Errorf("server selection cancelled")
	}
	if server == nil || result == nil {
		s.mu.Lock()
		s.cfg.ActiveServer = ""
		_ = s.persistConfigLocked()
		s.mu.Unlock()
		return "", fmt.Errorf("no enabled subscription has working internet")
	}
	s.commitSelectedServer(preferGroupMember(*server, result.SelectedMemberID))
	s.pm.event(serviceLogInfo, "priority", "start.selection",
		"сервер для запуска выбран",
		field("server", result.ServerName),
		field("latency_ms", result.LatencyMS),
		field("selection_mode", st.PingSelectionMode))
	return result.ServerName, nil
}

func (s *apiServer) commitSelectedServer(server VLESSServer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.upsertServerLocked(server)
	s.cfg.ActiveServer = server.ID
	_ = s.persistConfigLocked()
}

func preferGroupMember(server VLESSServer, memberID string) VLESSServer {
	if memberID == "" || len(server.Members) < 2 || server.Members[0].ID == memberID {
		return server
	}
	for i := 1; i < len(server.Members); i++ {
		if server.Members[i].ID != memberID {
			continue
		}
		members := append([]VLESSServer(nil), server.Members...)
		winner := members[i]
		copy(members[1:i+1], members[0:i])
		members[0] = winner
		server.Members = members
		server.Address = winner.Address
		server.Port = winner.Port
		return server
	}
	return server
}

// allServersLocked flattens effective priority groups for diagnostic "ping
// all" operations. Caller must hold s.mu (R or W).
func (s *apiServer) allServersLocked() []VLESSServer {
	groups := s.priorityServerGroupsLocked()
	var all []VLESSServer
	for _, group := range groups {
		all = append(all, group.Servers...)
	}
	return all
}

// refreshAllSubscriptions fetches every saved subscription URL and updates
// its server list in place. Network I/O happens unlocked; only the in-memory
// write of results is protected.
func (s *apiServer) refreshAllSubscriptions() {
	// Snapshot URLs+IDs+names under RLock.
	s.mu.RLock()
	type pending struct {
		id, name string
		sub      Subscription
	}
	work := make([]pending, 0, len(s.subs))
	for _, sub := range s.subs {
		if sub.Disabled {
			continue
		}
		item := pending{id: sub.ID, name: sub.Name, sub: *sub}
		item.sub.Servers = append([]VLESSServer(nil), sub.Servers...)
		work = append(work, item)
	}
	s.mu.RUnlock()

	// Fetch all in series (parallel would burst-load the modem).
	type fetched struct {
		id  string
		sub *Subscription
		err error
	}
	timeout := s.settingsSnapshot().SubscriptionFetchTimeout()
	results := make([]fetched, len(work))
	for i, w := range work {
		s.pm.log(serviceLogInfo, "[subscription] фоновое обновление %q: начинаю загрузку", w.name)
		var sub *Subscription
		var err error
		sub, err = s.fetchSubscriptionURLWithTimeout(w.sub.URL, timeout)
		results[i] = fetched{id: w.id, sub: sub, err: err}
		if err == nil {
			sub.ID = w.id
			preserveSubscriptionDisplayName(sub, &w.sub)
			preserveSubscriptionOptions(sub, &w.sub)
			migratedPings := s.pingCache.MigrateEquivalent(w.sub.Servers, sub.Servers)
			if migratedPings > 0 {
				s.pm.event(serviceLogInfo, "subscription", "ping_history.migrated",
					"история проверок перенесена на обновлённые записи серверов",
					field("subscription", w.name),
					field("servers", migratedPings))
			}
			s.pm.log(serviceLogInfo, "[subscription] фоновое обновление %q: %d серверов, %d исключено", w.name, len(sub.Servers), sub.ExcludedServers)
		} else {
			s.pm.log(serviceLogWarn, "[subscription] фоновое обновление %q не удалось: %v", w.name, err)
		}
	}

	// Apply under write lock.
	vpnRunning := s.pm.Status().Running
	s.mu.Lock()
	for _, r := range results {
		// Re-find by ID — subscription list may have shifted while we were fetching.
		for i, sub := range s.subs {
			if sub.ID != r.id {
				continue
			}
			if r.err != nil {
				s.subs[i].Error = r.err.Error()
			} else {
				s.subs[i] = r.sub
			}
			break
		}
	}
	_ = s.persistSubscriptionsLocked()
	// Re-sync manual cfg.Servers entries with whatever the subscriptions now
	// say about the same server ID. Without this, transport tuning knobs
	// that arrive via `extra={...}` (xmux, uplinkHTTPMethod, sc*Posts, …)
	// never reach the running sing-box even after a refresh because the
	// active server is looked up in cfg.Servers, not in s.subs.
	activeBefore := s.cfg.ActiveServer
	resynced := resyncServersFromSubs(s.cfg, s.subs)
	pruned := pruneStaleServersPreservingActive(s.cfg, s.subs)
	activeRemoved := activeBefore != "" && s.cfg.ActiveServer == ""
	if pruned > 0 || resynced > 0 || activeRemoved {
		_ = s.persistConfigLocked()
	}
	// Build keep-set for ping cache pruning.
	keep := map[string]bool{}
	for _, srv := range s.allServersLocked() {
		keep[srv.ID] = true
	}
	if vpnRunning && activeBefore != "" {
		keep[activeBefore] = true
	}
	s.mu.Unlock()

	// A background catalog refresh never controls tunnel lifetime. If the
	// active endpoint actually stops working, the health controller will
	// replace it after the configured consecutive failures.
	if pruned > 0 {
		s.pm.log(serviceLogInfo, "[subscription] удалено устаревших серверов: %d", pruned)
	}
	s.pingCache.Prune(keep)
}

// activeSubUserInfoLocked returns the quota info of the subscription
// containing the active server. Caller must hold s.mu (R or W).
func (s *apiServer) activeSubUserInfoLocked() *SubUserInfo {
	if s.cfg.ActiveServer == "" {
		return nil
	}
	active := s.cfg.activeServer()
	if active == nil {
		return nil
	}
	for _, sub := range s.subs {
		if sub.UserInfo == nil {
			continue
		}
		for _, srv := range sub.Servers {
			if sameCatalogServer(*active, srv) {
				return sub.UserInfo
			}
		}
	}
	return nil
}

// upsertServerLocked adds or updates a server in cfg.Servers.
// Caller must hold s.mu.Lock.
func (s *apiServer) upsertServerLocked(srv VLESSServer) {
	for i, existing := range s.cfg.Servers {
		if existing.ID == srv.ID {
			s.cfg.Servers[i] = srv
			_ = s.persistConfigLocked()
			return
		}
	}
	s.cfg.Servers = append(s.cfg.Servers, srv)
	_ = s.persistConfigLocked()
}

// connectServer sets the active server and starts it. Selecting "Connect" is
// an explicit command, so there is no separate "remember only" behaviour.
func (s *apiServer) connectServer(w http.ResponseWriter, id, name string) {
	s.mu.Lock()
	s.cfg.ActiveServer = id
	_ = s.persistConfigLocked()
	cfgSnap := *s.cfg
	s.mu.Unlock()

	wasRunning := s.pm.Status().Running
	if wasRunning {
		_ = s.pm.Stop()
		time.Sleep(300 * time.Millisecond)
		if err := s.connectStartFn(&cfgSnap); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	} else {
		if s.failover != nil {
			s.failover.ResetBackoff()
		}
		if err := s.connectStartFn(&cfgSnap); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	writeJSON(w, map[string]string{"status": "connected", "server": name})
}
