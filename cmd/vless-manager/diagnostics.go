package main

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"runtime/pprof"
	"strconv"
	"time"

	protocoludp "github.com/xtls/xray-core/common/protocol/udp"
)

// Profiling is opt-in, LAN-only and requires a real session even when normal
// UI authentication is disabled. Profiles stream to the client, not flash.
func (s *apiServer) allowDiagnostics(w http.ResponseWriter, r *http.Request) bool {
	if !s.diagnosticsEnabled {
		http.NotFound(w, r)
		return false
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "GET only")
		return false
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	address, parseErr := netip.ParseAddr(host)
	if err != nil || parseErr != nil || (!address.Unmap().IsPrivate() && !address.IsLoopback()) {
		writeError(w, http.StatusForbidden, "diagnostics require a LAN connection")
		return false
	}
	cookie, err := r.Cookie(authCookieName)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "diagnostics require authentication")
		return false
	}
	ttl := time.Duration(s.settingsSnapshot().AuthSessionTTLHours) * time.Hour
	if _, ok := s.auth.session(cookie.Value, ttl); !ok {
		writeError(w, http.StatusUnauthorized, "session expired")
		return false
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	return true
}

func (s *apiServer) handleRuntimeDiagnostics(w http.ResponseWriter, r *http.Request) {
	if !s.allowDiagnostics(w, r) {
		return
	}
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	proc := make(map[string]string)
	for _, name := range []string{"status", "io", "stat"} {
		if data, err := os.ReadFile("/proc/self/" + name); err == nil {
			proc[name] = string(data)
		}
	}
	for _, name := range []string{"mounts", "swaps", "meminfo"} {
		if data, err := os.ReadFile("/proc/" + name); err == nil {
			proc[name] = string(data)
		}
	}
	writeJSON(w, map[string]any{
		"go_arch": runtime.GOARCH, "go_version": runtime.Version(),
		"go_cpu_limit": runtime.GOMAXPROCS(0), "goroutines": runtime.NumGoroutine(),
		"memory_limit_bytes": debug.SetMemoryLimit(-1), "memory": memory,
		"gogc": os.Getenv("GOGC"), "runtime_dir": s.runtimeDir,
		"data_dir": filepath.Dir(s.cfgPath), "proc": proc,
		"udp": protocoludp.DiagnosticSnapshot(),
	})
}

func (s *apiServer) handleCPUProfile(w http.ResponseWriter, r *http.Request) {
	if !s.allowDiagnostics(w, r) {
		return
	}
	seconds := 10
	if value := r.URL.Query().Get("seconds"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 15 {
			writeError(w, http.StatusBadRequest, "seconds must be between 1 and 15")
			return
		}
		seconds = parsed
	}
	if !s.profileBusy.CompareAndSwap(false, true) {
		writeError(w, http.StatusConflict, "a profile is already running")
		return
	}
	defer s.profileBusy.Store(false)
	// A slow or disconnected client must not hold the profiler indefinitely.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(20 * time.Second))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="cpu.pprof"`)
	if err := pprof.StartCPUProfile(w); err != nil {
		writeError(w, http.StatusConflict, fmt.Sprintf("start CPU profile: %v", err))
		return
	}
	defer pprof.StopCPUProfile()
	timer := time.NewTimer(time.Duration(seconds) * time.Second)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-r.Context().Done():
	}
}

func (s *apiServer) handleHeapProfile(w http.ResponseWriter, r *http.Request) {
	if !s.allowDiagnostics(w, r) {
		return
	}
	if !s.profileBusy.CompareAndSwap(false, true) {
		writeError(w, http.StatusConflict, "a profile is already running")
		return
	}
	defer s.profileBusy.Store(false)
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(20 * time.Second))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="heap.pprof"`)
	// Do not force GC on a live low-memory router just to take a snapshot.
	_ = pprof.WriteHeapProfile(w)
}

func (s *apiServer) handleGoroutineProfile(w http.ResponseWriter, r *http.Request) {
	if !s.allowDiagnostics(w, r) {
		return
	}
	if !s.profileBusy.CompareAndSwap(false, true) {
		writeError(w, http.StatusConflict, "a profile is already running")
		return
	}
	defer s.profileBusy.Store(false)
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(20 * time.Second))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="goroutines.pprof"`)
	_ = pprof.Lookup("goroutine").WriteTo(w, 0)
}
