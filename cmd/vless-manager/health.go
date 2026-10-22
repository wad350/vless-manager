package main

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
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

// WAN readiness is local: a whitelist must not prevent the VPN from starting.
// /proc/net/route exposes the main IPv4 table, not our TPROXY table 100.
func wanRouteReady() bool {
	routes, err := os.Open("/proc/net/route")
	if err != nil {
		return false
	}
	defer routes.Close()
	return defaultWANRouteReady(routes, func(name string) bool {
		iface, err := net.InterfaceByName(name)
		if err != nil || iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			return false
		}
		addrs, err := iface.Addrs()
		if err != nil {
			return false
		}
		for _, addr := range addrs {
			ip, _, err := net.ParseCIDR(addr.String())
			if err == nil && ip.To4() != nil && !ip.IsUnspecified() && !ip.IsLoopback() {
				return true
			}
		}
		return false
	})
}

func defaultWANRouteReady(routes io.Reader, interfaceReady func(string) bool) bool {
	const routeUp, routeReject = 0x1, 0x200
	scanner := bufio.NewScanner(routes)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 8 || fields[0] == "lo" || fields[0] == tunIface {
			continue
		}
		destination, dstErr := strconv.ParseUint(fields[1], 16, 32)
		mask, maskErr := strconv.ParseUint(fields[7], 16, 32)
		flags, flagsErr := strconv.ParseUint(fields[3], 16, 32)
		if dstErr == nil && maskErr == nil && flagsErr == nil && destination == 0 && mask == 0 &&
			flags&routeUp != 0 && flags&routeReject == 0 && interfaceReady(fields[0]) {
			return true
		}
	}
	return false
}

// WaitForWAN waits for an IPv4 default route and an up, addressed interface.
// Operator reachability remains the failover controller's responsibility.
func WaitForWAN(timeout time.Duration) bool {
	return waitForWAN(timeout, wanRouteReady)
}

func waitForWAN(timeout time.Duration, ready func() bool) bool {
	deadline := time.Now().Add(timeout)
	delays := []time.Duration{500 * time.Millisecond, 1 * time.Second, 2 * time.Second, 3 * time.Second}
	i := 0
	for time.Now().Before(deadline) {
		if ready() {
			return true
		}
		d := delays[len(delays)-1]
		if i < len(delays) {
			d = delays[i]
		}
		i++
		if remaining := time.Until(deadline); d > remaining {
			d = remaining
		}
		if d <= 0 {
			return false
		}
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
