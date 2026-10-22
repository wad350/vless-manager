package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"syscall"
	"time"
)

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
// (bypassing the VPN tunnel). It is derived from the operator controller's
// latest open-internet probes, not a separate network check.
type InternetStatus struct {
	OK        bool          `json:"ok"`
	Reachable int           `json:"reachable"`
	Total     int           `json:"total"`
	LatencyMS int64         `json:"latency_ms"`
	CheckedAt time.Time     `json:"checked_at"`
	Probes    []ProbeResult `json:"probes"`
}

func (s FailoverState) InternetStatus() InternetStatus {
	reachable := 0
	bestLat := int64(-1)
	for _, p := range s.OpenProbes {
		if p.OK {
			reachable++
			if bestLat < 0 || p.LatencyMS < bestLat {
				bestLat = p.LatencyMS
			}
		}
	}
	return InternetStatus{
		OK:        reachable > 0,
		Reachable: reachable,
		Total:     len(s.OpenProbes),
		LatencyMS: bestLat,
		CheckedAt: s.LastCheck,
		Probes:    s.OpenProbes,
	}
}

// wanDialer returns a net.Dialer that marks all outgoing sockets with
// WANFwmark via SO_MARK. Combined with the "fwmark X lookup main" ip rule
// added in EnableGlobalRoute, this forces these connections to bypass TPROXY
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

func wanProbeNetwork(network string) string {
	if network == "tcp" {
		return "tcp4"
	}
	return network
}

// wanDialContext forces IPv4 for WAN-only HTTP requests. The managed policy
// route is IPv4-only; accepting an AAAA result here can produce a false
// "network is unreachable" even when the same host is reachable over IPv4.
func wanDialContext(timeout time.Duration) func(context.Context, string, string) (net.Conn, error) {
	dialer := wanDialer(timeout)
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		return dialer.DialContext(ctx, wanProbeNetwork(network), address)
	}
}

// WaitForWAN blocks until a WAN-only HTTP probe succeeds or the timeout
// elapses. Used at boot to defer starting Xray until the modem has
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
			DialContext:       wanDialContext(timeout),
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
