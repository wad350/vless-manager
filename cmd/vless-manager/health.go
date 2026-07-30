package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"sync"
	"syscall"
	"time"
)

// healthCheckURLs are the public endpoints we probe to decide whether the
// underlying WAN connection has real internet (not just operator-whitelisted
// services like ya.ru / mail.ru). We avoid Google / Yandex specifically
// because they're often zero-rated / on carrier whitelists and would give
// false positives when the rest of the internet is unreachable.
var healthCheckURLs = []string{
	"http://cp.cloudflare.com/generate_204",
	"http://detectportal.firefox.com/success.txt",
	"http://nmcheck.gnome.org/check_network_status.txt",
}

// wanProbeCall is a test seam for connectivity decision logic. Production
// always points at wanProbe; tests replace it without touching real routes.
var wanProbeCall = wanProbe

// soMark is the SO_MARK socket option (Linux). Hardcoded because Go's syscall
// package on macOS (build host) doesn't export this Linux-only constant.
const soMark = 36

// ProbeResult is the outcome for a single endpoint in the WAN health check.
type ProbeResult struct {
	URL       string `json:"url"`
	OK        bool   `json:"ok"`
	LatencyMS int64  `json:"latency_ms"`
	Error     string `json:"error,omitempty"`
}

// InternetStatus reports whether real internet is reachable directly via WAN
// (bypassing the VPN tunnel). At least one of three non-whitelisted endpoints
// must succeed for "ok".
type InternetStatus struct {
	OK        bool          `json:"ok"`
	Reachable int           `json:"reachable"`
	Total     int           `json:"total"`
	LatencyMS int64         `json:"latency_ms"`
	CheckedAt time.Time     `json:"checked_at"`
	Probes    []ProbeResult `json:"probes"`
}

// healthMonitor stores the most recent WAN connectivity check. Refreshed by
// a background goroutine.
//
// Probe URLs and per-probe timeout come from AppSettings (via settingsFn so we
// don't need a back-pointer to *apiServer). Falling back to defaults when the
// user empties the list keeps the UI responsive even with a misconfiguration.
type healthMonitor struct {
	mu         sync.RWMutex
	status     InternetStatus
	settingsFn func() AppSettings
}

func newHealthMonitor() *healthMonitor {
	return &healthMonitor{status: InternetStatus{LatencyMS: -1}}
}

// SetSettingsSource wires the snapshot function used to read URLs/timeouts.
// Called once during apiServer init.
func (h *healthMonitor) SetSettingsSource(fn func() AppSettings) {
	h.mu.Lock()
	h.settingsFn = fn
	h.mu.Unlock()
}

func (h *healthMonitor) Status() InternetStatus {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.status
}

// Check probes every healthCheckURL in parallel via WAN-only dialer and
// stores the aggregated result.
func (h *healthMonitor) Check() InternetStatus {
	urls := healthCheckURLs
	timeout := 5 * time.Second
	h.mu.RLock()
	fn := h.settingsFn
	h.mu.RUnlock()
	if fn != nil {
		st := fn()
		if len(st.OpenProbes) > 0 {
			urls = st.OpenProbes
		}
		if t := st.InternetCheckTimeout(); t > 0 {
			timeout = t
		}
	}
	probes := make([]ProbeResult, len(urls))
	var wg sync.WaitGroup
	for i, u := range urls {
		wg.Add(1)
		go func(i int, u string) {
			defer wg.Done()
			start := time.Now()
			ok, err := wanProbeCall(u, timeout)
			pr := ProbeResult{URL: u, OK: ok, LatencyMS: time.Since(start).Milliseconds()}
			if err != nil {
				pr.Error = err.Error()
				pr.LatencyMS = -1
			}
			probes[i] = pr
		}(i, u)
	}
	wg.Wait()

	reachable := 0
	bestLat := int64(-1)
	for _, p := range probes {
		if p.OK {
			reachable++
			if bestLat < 0 || p.LatencyMS < bestLat {
				bestLat = p.LatencyMS
			}
		}
	}
	status := InternetStatus{
		OK:        reachable > 0,
		Reachable: reachable,
		Total:     len(probes),
		LatencyMS: bestLat,
		CheckedAt: time.Now(),
		Probes:    probes,
	}
	h.mu.Lock()
	h.status = status
	h.mu.Unlock()
	return status
}

// wanDialer returns a net.Dialer that marks all outgoing sockets with
// WANFwmark via SO_MARK. Combined with the "fwmark X lookup main" ip rule
// added in EnableGlobalRoute, this forces these connections to bypass tun0
// and use the main routing table (WAN).
func wanDialer(timeout time.Duration) *net.Dialer {
	return &net.Dialer{
		Timeout: timeout,
		Control: func(network, address string, c syscall.RawConn) error {
			var serr error
			err := c.Control(func(fd uintptr) {
				serr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, soMark, WANFwmark)
			})
			if err != nil {
				return err
			}
			return serr
		},
	}
}

// WaitForWAN blocks until a WAN-only HTTP probe succeeds or the timeout
// elapses. Used at boot to defer starting sing-box until the modem has
// finished bringing the default route up.
//
// Uses exponential backoff (0.5 s → 1 s → 2 s → 3 s, then constant) to
// converge quickly when the modem boots fast (cached UICC) without hammering
// the network when it's genuinely down.
func WaitForWAN(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	delays := []time.Duration{500 * time.Millisecond, 1 * time.Second, 2 * time.Second, 3 * time.Second}
	i := 0
	for time.Now().Before(deadline) {
		if ok, _ := wanProbeCall("http://cp.cloudflare.com/generate_204", 3*time.Second); ok {
			return true
		}
		d := delays[len(delays)-1]
		if i < len(delays) {
			d = delays[i]
		}
		i++
		time.Sleep(d)
	}
	return false
}

// wanProbe performs a one-shot HTTP GET via the WAN-only dialer. Returns
// ok=true if the server responded at all (any status < 500). The point is
// "is the site reachable via WAN", so 2xx/3xx/4xx are all valid — what we
// care about is the TCP+TLS+HTTP roundtrip completing.
//   - generate_204 returns 204
//   - ya.ru returns 302 → HTTPS
//   - mail.ru returns 301
//   - vk.com returns 418 (their teapot easter egg)
//
// All of those mean "server alive".
func wanProbe(url string, timeout time.Duration) (bool, error) {
	client := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext:       wanDialer(timeout).DialContext,
			DisableKeepAlives: true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return false, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return resp.StatusCode < 500, nil
}
