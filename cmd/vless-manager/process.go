package main

import (
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const logBufSize = 500

type serviceLogLevel int

const (
	serviceLogError serviceLogLevel = iota
	serviceLogWarn
	serviceLogInfo
	serviceLogDebug
	serviceLogTrace
)

func parseServiceLogLevel(value string) serviceLogLevel {
	switch value {
	case "error":
		return serviceLogError
	case "warn":
		return serviceLogWarn
	case "debug":
		return serviceLogDebug
	case "trace":
		return serviceLogTrace
	default:
		return serviceLogInfo
	}
}

func serviceLogLevelName(level serviceLogLevel) string {
	switch level {
	case serviceLogError:
		return "ERROR"
	case serviceLogWarn:
		return "WARN"
	case serviceLogDebug:
		return "DEBUG"
	case serviceLogTrace:
		return "TRACE"
	default:
		return "INFO"
	}
}

type ringBuffer struct {
	mu       sync.Mutex
	entries  []serviceLogEntry
	head     int
	count    int
	seq      int
	minLevel serviceLogLevel
}

type serviceLogEntry struct {
	Seq       int               `json:"seq"`
	Timestamp time.Time         `json:"timestamp"`
	Level     string            `json:"level"`
	Component string            `json:"component"`
	Event     string            `json:"event"`
	Message   string            `json:"message"`
	Fields    map[string]string `json:"fields,omitempty"`
}

type logField struct {
	key   string
	value any
}

func field(key string, value any) logField {
	return logField{key: key, value: value}
}

func newRingBuffer() *ringBuffer {
	return &ringBuffer{
		entries:  make([]serviceLogEntry, logBufSize),
		minLevel: serviceLogInfo,
	}
}

func (r *ringBuffer) setLevel(value string) {
	r.mu.Lock()
	r.minLevel = parseServiceLogLevel(value)
	r.mu.Unlock()
}

func (r *ringBuffer) clear() {
	r.mu.Lock()
	clear(r.entries)
	r.head = 0
	r.count = 0
	r.mu.Unlock()
}

func (r *ringBuffer) log(level serviceLogLevel, line string) {
	component, message := splitLogComponent(line)
	r.logEvent(level, component, "message", message)
}

func splitLogComponent(line string) (string, string) {
	if strings.HasPrefix(line, "[") {
		if end := strings.IndexByte(line, ']'); end > 1 {
			return line[1:end], strings.TrimSpace(line[end+1:])
		}
	}
	return "manager", line
}

func (r *ringBuffer) logEvent(level serviceLogLevel, component, event, message string, fields ...logField) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if level > r.minLevel {
		return
	}
	r.appendEventLocked(level, component, event, message, fields...)
}

// logEventUnfiltered is used for Xray messages. The Xray logger
// already applies its own independently configured level, so applying the
// manager level again would make the two UI settings interfere.
func (r *ringBuffer) logEventUnfiltered(level serviceLogLevel, component, event, message string, fields ...logField) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.appendEventLocked(level, component, event, message, fields...)
}

func (r *ringBuffer) appendEventLocked(level serviceLogLevel, component, event, message string, fields ...logField) {
	r.seq++
	entry := serviceLogEntry{
		Seq:       r.seq,
		Timestamp: time.Now(),
		Level:     serviceLogLevelName(level),
		Component: component,
		Event:     event,
		Message:   message,
	}
	if len(fields) > 0 {
		entry.Fields = make(map[string]string, len(fields))
		for _, f := range fields {
			if f.key != "" && f.value != nil {
				if value, ok := f.value.(string); ok {
					entry.Fields[f.key] = value
				} else {
					entry.Fields[f.key] = fmt.Sprint(f.value)
				}
			}
		}
	}
	index := (r.head + r.count) % logBufSize
	if r.count == logBufSize {
		index = r.head
		r.head = (r.head + 1) % logBufSize
	} else {
		r.count++
	}
	r.entries[index] = entry
}

func formatServiceLogEntry(entry serviceLogEntry) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s level=%s component=%s event=%s",
		entry.Timestamp.Format(time.RFC3339Nano),
		entry.Level,
		entry.Component,
		entry.Event,
	)
	keys := make([]string, 0, len(entry.Fields))
	for key := range entry.Fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		fmt.Fprintf(&b, " %s=%s", key, strconv.Quote(entry.Fields[key]))
	}
	if entry.Message != "" {
		fmt.Fprintf(&b, " msg=%s", strconv.Quote(entry.Message))
	}
	return b.String()
}

func (r *ringBuffer) Entries(since int) ([]serviceLogEntry, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	seq := r.seq
	if since >= seq || r.count == 0 {
		return nil, seq
	}
	start := seq - r.count
	offset := 0
	if since > start {
		offset = since - start
	}
	entries := make([]serviceLogEntry, r.count-offset)
	for i := range entries {
		entries[i] = r.entries[(r.head+offset+i)%logBufSize]
	}
	return entries, seq
}

type ProcessStatus struct {
	Generation  uint64 `json:"-"`
	Running     bool   `json:"running"`
	RouteReady  bool   `json:"route_ready"`
	PID         int    `json:"pid"`
	UptimeSec   int64  `json:"uptime_sec"`
	Error       string `json:"error,omitempty"`
	RoutingMode string `json:"routing_mode,omitempty"`
}

type outboundTrafficSnapshot struct {
	VPNDownload    uint64
	VPNUpload      uint64
	BypassDownload uint64
	BypassUpload   uint64
}

// ProcessManager embeds Xray as a library instead of running it as a
// separate process. This avoids a second runtime and keeps control, traffic
// accounting and network-engine logs in the same process.
type ProcessManager struct {
	lifecycleMu sync.Mutex
	mu          sync.Mutex
	box         engineHandle
	logs        *ringBuffer
	running     bool
	startedAt   time.Time
	lastErr     string
	routeHost   string
	routeReady  bool
	routeMode   routeMode
	guardOnce   sync.Once
	opSeq       atomic.Uint64
	generation  uint64
}

func NewProcessManager(dataDir string) *ProcessManager {
	return &ProcessManager{
		logs: newRingBuffer(),
	}
}

func (pm *ProcessManager) SetServiceLogLevel(level string) {
	pm.logs.setLevel(level)
}

func (pm *ProcessManager) ClearLogs() {
	pm.logs.clear()
}

func (pm *ProcessManager) log(level serviceLogLevel, format string, args ...any) {
	pm.logs.log(level, fmt.Sprintf(format, args...))
}

func (pm *ProcessManager) event(level serviceLogLevel, component, event, message string, fields ...logField) {
	pm.logs.logEvent(level, component, event, message, fields...)
}

func (pm *ProcessManager) nextOperationID(prefix string) string {
	return fmt.Sprintf("%s-%06d", prefix, pm.opSeq.Add(1))
}

func (pm *ProcessManager) Start(cfg *Config) error {
	pm.lifecycleMu.Lock()
	defer pm.lifecycleMu.Unlock()
	started := time.Now()
	pm.mu.Lock()
	defer pm.mu.Unlock()

	if pm.running {
		return fmt.Errorf("already running")
	}

	srv := cfg.activeServer()
	if srv == nil {
		return fmt.Errorf("no active server configured")
	}

	dialServer, dialIP, err := pinVLESSServer(srv)
	if err != nil {
		return fmt.Errorf("resolve active server: %w", err)
	}

	if err := ensureTProxySupport(run); err != nil {
		pm.lastErr = err.Error()
		return fmt.Errorf("TPROXY unavailable: %w", err)
	}
	data, err := generateXrayConfig(cfg, dialServer)
	if err != nil {
		return fmt.Errorf("generate Xray config: %w", err)
	}

	bh, err := startEmbedded(data, pm.logs)
	if err != nil {
		pm.lastErr = err.Error()
		pm.event(serviceLogError, "manager", "vpn.runtime_start_failed",
			"Xray не запущен",
			field("server", srv.Name),
			field("error", err),
			field("duration_ms", time.Since(started).Milliseconds()))
		return err
	}

	bypassCount := bypassEffectiveCount(cfg)
	if err := EnableGlobalRoute(dialIP); err != nil {
		closeErr := bh.Close()
		pm.lastErr = err.Error()
		pm.event(serviceLogError, "routing", "global.enable_failed",
			"маршрутизация не включена, Xray остановлен",
			field("server_address", srv.Address),
			field("error", err),
			field("close_error", closeErr),
			field("duration_ms", time.Since(started).Milliseconds()))
		return fmt.Errorf("enable global route: %w", err)
	}

	pm.box = bh
	pm.running = true
	pm.generation++
	pm.startedAt = time.Now()
	pm.lastErr = ""
	pm.routeHost = dialIP
	pm.routeReady = true
	pm.routeMode = routeTPROXY
	pm.guardOnce.Do(func() { go pm.routeGuard() })
	pm.event(serviceLogInfo, "routing", "global.enabled",
		"глобальная маршрутизация включена",
		field("server_address", srv.Address),
		field("dial_ip", dialIP),
		field("routing_mode", routeTPROXY),
		field("bypass_domains", bypassCount))
	pm.event(serviceLogInfo, "manager", "vpn.started",
		"VPN запущен и направляет трафик",
		field("server", srv.Name),
		field("address", srv.Address),
		field("dial_ip", dialIP),
		field("transport", srv.Network),
		field("bypass_domains", bypassCount),
		field("duration_ms", time.Since(started).Milliseconds()))
	return nil
}

func (pm *ProcessManager) Stop() error {
	pm.lifecycleMu.Lock()
	defer pm.lifecycleMu.Unlock()
	pm.mu.Lock()
	if !pm.running || pm.box == nil {
		pm.mu.Unlock()
		return nil
	}
	pm.event(serviceLogInfo, "manager", "vpn.stop_requested", "остановка Xray")
	DisableGlobalRoute()
	bh := pm.box
	pm.box = nil
	pm.running = false
	pm.generation++
	pm.routeHost = ""
	pm.routeReady = false
	pm.mu.Unlock()

	if err := bh.Close(); err != nil {
		pm.mu.Lock()
		pm.lastErr = err.Error()
		pm.mu.Unlock()
		pm.event(serviceLogError, "manager", "vpn.stop_failed",
			"ошибка остановки Xray", field("error", err))
		return err
	} else {
		pm.event(serviceLogInfo, "manager", "vpn.stopped", "Xray остановлен")
	}
	return nil
}

func (pm *ProcessManager) TunnelRunning() bool {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	return pm.running
}

func (pm *ProcessManager) UpdateBypassRules(cfg *Config) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if !pm.running || pm.box == nil {
		return nil
	}
	updater, ok := pm.box.(interface{ UpdateBypassRules(*Config) error })
	if !ok {
		return fmt.Errorf("running engine does not support routing updates")
	}
	return updater.UpdateBypassRules(cfg)
}

func (pm *ProcessManager) TrafficSnapshot() (outboundTrafficSnapshot, bool) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if !pm.running || pm.box == nil {
		return outboundTrafficSnapshot{}, false
	}
	return pm.box.TrafficSnapshot(), true
}

func (pm *ProcessManager) Status() ProcessStatus {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	s := ProcessStatus{
		Generation: pm.generation,
		Running:    pm.running,
		RouteReady: pm.routeReady,
		Error:      pm.lastErr,
	}
	if pm.running {
		s.UptimeSec = int64(time.Since(pm.startedAt).Seconds())
		s.RoutingMode = string(pm.routeMode)
	}
	return s
}

// pinVLESSServer resolves the endpoint once per tunnel start. Keeping a
// hostname in the outbound makes every new connection depend on the router's
// DNS resolver; during an outage those lookups accumulate and can starve a
// small router. TLS SNI and HTTP Host remain tied to the original hostname.
func pinVLESSServer(srv *VLESSServer) (*VLESSServer, string, error) {
	if len(srv.Members) > 0 {
		pinned := *srv
		pinned.Members = make([]VLESSServer, 0, len(srv.Members))
		firstIP := ""
		for i := range srv.Members {
			member, dialIP, err := pinVLESSServer(&srv.Members[i])
			if err != nil {
				continue
			}
			if firstIP == "" {
				firstIP = dialIP
				pinned.Address = member.Address
				pinned.Port = member.Port
			}
			pinned.Members = append(pinned.Members, *member)
		}
		if len(pinned.Members) == 0 {
			return nil, "", fmt.Errorf("profile %q has no resolvable members", srv.Name)
		}
		return &pinned, firstIP, nil
	}
	addrs := ResolveAddrs(srv.Address)
	if len(addrs) == 0 {
		return nil, "", fmt.Errorf("%q has no IPv4 address", srv.Address)
	}

	pinned := *srv
	pinned.Address = addrs[0]
	if net.ParseIP(srv.Address) == nil {
		if pinned.SNI == "" && (srv.Security == "tls" || srv.Security == "reality") {
			pinned.SNI = srv.Address
		}
		if pinned.Host == "" {
			switch srv.Network {
			case "ws", "http", "h2", "httpupgrade":
				pinned.Host = srv.Address
			}
		}
	}
	return &pinned, addrs[0], nil
}

func (pm *ProcessManager) routeGuard() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		pm.mu.Lock()
		if !pm.running || pm.routeHost == "" {
			pm.mu.Unlock()
			continue
		}
		if GlobalRouteReady() {
			pm.routeReady = true
			pm.mu.Unlock()
			continue
		}

		pm.routeReady = false
		endpoint := pm.routeHost
		pm.event(serviceLogWarn, "routing", "global.rules_missing",
			"правила маршрутизации VPN исчезли; выполняется восстановление",
			field("endpoint", endpoint))
		err := EnableGlobalRoute(endpoint)
		if err != nil {
			pm.lastErr = err.Error()
			pm.event(serviceLogError, "routing", "global.repair_failed",
				"не удалось восстановить правила маршрутизации VPN",
				field("endpoint", endpoint),
				field("error", err))
		} else {
			pm.routeReady = true
			pm.event(serviceLogInfo, "routing", "global.repaired",
				"правила маршрутизации VPN восстановлены",
				field("endpoint", endpoint))
		}
		pm.mu.Unlock()
	}
}

func formatUptime(secs int64) string {
	if secs < 60 {
		return fmt.Sprintf("%ds", secs)
	}
	m := secs / 60
	s := secs % 60
	if m < 60 {
		return fmt.Sprintf("%dm%02ds", m, s)
	}
	h := m / 60
	m = m % 60
	return fmt.Sprintf("%dh%02dm%02ds", h, m, s)
}
