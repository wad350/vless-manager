package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"sync"
	"time"

	"golang.org/x/net/proxy"
)

// defaultPingTestURL is the fallback target when AppSettings.PingTestURL is
// blank (e.g. before fillDefaults runs). Matches the "ping URL" v2rayN/happ use.
const defaultPingTestURL = "http://www.gstatic.com/generate_204"

// PingResult is the latency / availability / compatibility of one VLESS
// endpoint as measured by an HTTP GET via a temporary sing-box that uses
// THIS specific server as its outbound. Marked incompatible upfront if the
// transport is not supported by sing-box.
type PingResult struct {
	ServerID         string    `json:"server_id"`
	ServerName       string    `json:"server_name"`
	Address          string    `json:"address"`
	Port             int       `json:"port"`
	Protocol         string    `json:"protocol"`
	LatencyMS        int64     `json:"latency_ms"` // -1 = unreachable
	Error            string    `json:"error,omitempty"`
	Incompat         bool      `json:"incompatible,omitempty"`
	CheckedAt        time.Time `json:"checked_at"`
	SelectedMemberID string    `json:"selected_member_id,omitempty"`
}

// supportedNetworks — VLESS transports sing-box can actually speak.
var supportedNetworks = map[string]bool{
	"":            true,
	"tcp":         true,
	"ws":          true,
	"grpc":        true,
	"h2":          true,
	"http":        true,
	"httpupgrade": true,
	"quic":        true,
	"xhttp":       false,
}

func isSupportedServer(srv *VLESSServer) bool {
	if len(srv.Members) > 0 {
		return true
	}
	return supportedNetworks[normalizeVLESSNetwork(srv.Network)]
}

func describeProtocol(srv *VLESSServer) string {
	if len(srv.Members) > 0 {
		return fmt.Sprintf("VLESS auto (%d узлов)", len(srv.Members))
	}
	sec := srv.Security
	if sec == "" || sec == "none" {
		sec = "plain"
	}
	switch sec {
	case "reality":
		sec = "Reality"
	case "tls":
		sec = "TLS"
	}
	net := normalizeVLESSNetwork(srv.Network)
	return fmt.Sprintf("VLESS %s (%s)", sec, net)
}

// pingViaSOCKS performs HTTP GET → testURL through a SOCKS5 proxy.
// testURL may be blank — defaultPingTestURL is used in that case.
func pingViaSOCKS(srv *VLESSServer, socksPort int, timeout time.Duration, testURL string) PingResult {
	return pingViaSOCKSContext(context.Background(), srv, socksPort, timeout, testURL)
}

func pingViaSOCKSContext(ctx context.Context, srv *VLESSServer, socksPort int, timeout time.Duration, testURL string) PingResult {
	res := PingResult{
		ServerID:   srv.ID,
		ServerName: srv.Name,
		Address:    srv.Address,
		Port:       srv.Port,
		LatencyMS:  -1,
	}
	dialer, err := proxy.SOCKS5("tcp", fmt.Sprintf("127.0.0.1:%d", socksPort), nil, proxy.Direct)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	client := &http.Client{
		Transport: &http.Transport{
			Dial:              dialer.Dial,
			DisableKeepAlives: true,
		},
		Timeout: timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	if testURL == "" {
		testURL = defaultPingTestURL
	}
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, testURL, nil)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	resp, err := client.Do(req)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	res.LatencyMS = time.Since(start).Milliseconds()
	return res
}

// pingBatchViaSingBox tests each server with a real VLESS connection by
// starting a temporary embedded sing-box per probe (SOCKS5 inbound + VLESS
// outbound), measuring HTTP GET → testURL through it, then closing.
//
// `maxParallel` controls concurrency:
//
//	<= 1 → sequential (default; conservative on 124 MB MIPS routers)
//	>= 2 → fan-out via a semaphore. Each parallel slot adds ~30 MB RSS so
//	       only crank this up when you've measured the headroom.
//
// onDone is invoked per server as its result lands; may be nil.
func pingBatchViaSingBox(servers []VLESSServer, timeout time.Duration, testURL string, maxParallel int, onDone func(int, PingResult)) []PingResult {
	return pingBatchViaSingBoxContext(context.Background(), servers, timeout, testURL, maxParallel, onDone)
}

func pingBatchViaSingBoxContext(ctx context.Context, servers []VLESSServer, timeout time.Duration, testURL string, maxParallel int, onDone func(int, PingResult)) []PingResult {
	if containsServerGroup(servers) {
		return pingServerGroups(ctx, servers, timeout, testURL, maxParallel, onDone)
	}
	results := make([]PingResult, len(servers))
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
	}
	if maxParallel < 1 {
		maxParallel = 1
	}
	if maxParallel == 1 {
		for i := range servers {
			if ctx.Err() != nil {
				break
			}
			if results[i].Incompat {
				continue
			}
			r := pingOneThroughVLESSContext(ctx, &servers[i], timeout, testURL)
			results[i] = r
			if onDone != nil {
				onDone(i, r)
			}
		}
		return results
	}
	// Bounded fan-out — each goroutine blocks until a semaphore slot frees,
	// so we never run more than maxParallel temp sing-boxes at once.
	sem := make(chan struct{}, maxParallel)
	var wg sync.WaitGroup
	for i := range servers {
		if results[i].Incompat {
			continue
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()
			if ctx.Err() != nil {
				return
			}
			r := pingOneThroughVLESSContext(ctx, &servers[i], timeout, testURL)
			results[i] = r
			if onDone != nil {
				onDone(i, r)
			}
		}(i)
	}
	wg.Wait()
	return results
}

func containsServerGroup(servers []VLESSServer) bool {
	for i := range servers {
		if len(servers[i].Members) > 0 {
			return true
		}
	}
	return false
}

// pingServerGroups probes every distinct physical endpoint once and derives a
// logical profile result from its fastest reachable member. Provider auto
// profiles heavily overlap; probing each profile independently would turn 50
// endpoints into hundreds of temporary sing-box instances on the router.
func pingServerGroups(ctx context.Context, servers []VLESSServer, timeout time.Duration, testURL string, maxParallel int, onDone func(int, PingResult)) []PingResult {
	leaves := make([]VLESSServer, 0, len(servers))
	seen := make(map[string]bool)
	for _, srv := range servers {
		members := srv.Members
		if len(members) == 0 {
			members = []VLESSServer{srv}
		}
		for _, member := range members {
			if !seen[member.ID] {
				seen[member.ID] = true
				leaves = append(leaves, member)
			}
		}
	}
	leafResults := pingBatchViaSingBoxContext(ctx, leaves, timeout, testURL, maxParallel, nil)
	byID := make(map[string]PingResult, len(leafResults))
	for _, result := range leafResults {
		byID[result.ServerID] = result
	}

	results := make([]PingResult, len(servers))
	for i, srv := range servers {
		if len(srv.Members) == 0 {
			results[i] = byID[srv.ID]
		} else {
			result := PingResult{ServerID: srv.ID, ServerName: srv.Name, Address: srv.Address, Port: srv.Port, Protocol: describeProtocol(&srv), LatencyMS: -1, CheckedAt: time.Now()}
			for _, member := range srv.Members {
				candidate := byID[member.ID]
				if candidate.LatencyMS >= 0 && (result.LatencyMS < 0 || candidate.LatencyMS < result.LatencyMS) {
					result.LatencyMS = candidate.LatencyMS
					result.Address = candidate.Address
					result.Port = candidate.Port
					result.Error = ""
					result.SelectedMemberID = member.ID
				}
			}
			if result.LatencyMS < 0 {
				result.Error = "ни один узел профиля не отвечает"
			}
			results[i] = result
		}
		if onDone != nil {
			onDone(i, results[i])
		}
	}
	return results
}

// pingTCPFallback — raw TCP-connect to VLESS host:port, used as fallback
// when a temporary sing-box instance cannot be started.
func pingTCPFallback(srv *VLESSServer, timeout time.Duration) PingResult {
	res := PingResult{
		ServerID:   srv.ID,
		ServerName: srv.Name,
		Address:    srv.Address,
		Port:       srv.Port,
		Protocol:   describeProtocol(srv),
		LatencyMS:  -1,
		CheckedAt:  time.Now(),
	}
	start := time.Now()
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", srv.Address, srv.Port), timeout)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	conn.Close()
	res.LatencyMS = time.Since(start).Milliseconds()
	return res
}

func fastestReachable(results []PingResult) *PingResult {
	for i := range results {
		if results[i].LatencyMS >= 0 && !results[i].Incompat {
			return &results[i]
		}
	}
	return nil
}

// sortByLatency: reachable (asc) → unreachable → incompatible.
func sortByLatency(results []PingResult) {
	sort.Slice(results, func(i, j int) bool {
		ri, rj := &results[i], &results[j]
		if ri.Incompat != rj.Incompat {
			return !ri.Incompat
		}
		li, lj := ri.LatencyMS, rj.LatencyMS
		if li < 0 && lj < 0 {
			return false
		}
		if li < 0 {
			return false
		}
		if lj < 0 {
			return true
		}
		return li < lj
	})
}
