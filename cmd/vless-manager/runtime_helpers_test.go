package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSettingsDurationAccessors(t *testing.T) {
	s := defaultSettings()
	if s.OuterInterval() != 30*time.Second ||
		s.HealthInterval() != 10*time.Second ||
		s.ProbeTimeout() != 4*time.Second ||
		s.StartBackoff() != 300*time.Second ||
		s.HealthTimeout() != 6*time.Second ||
		s.WaitForWANTimeout() != 180*time.Second ||
		s.InternetCheckInterval() != time.Hour ||
		s.InternetCheckTimeout() != 5*time.Second ||
		s.PingTimeout() != 30*time.Second ||
		s.SubscriptionRefreshInterval() != time.Hour ||
		s.SubscriptionFetchTimeout() != 15*time.Second ||
		s.PingStartupSleep() != 300*time.Millisecond ||
		s.PingCacheMaxAge() != 60*time.Minute {
		t.Fatalf("unexpected durations: %+v", s)
	}
}

func TestHealthMonitorAndWaitForWANWithDeterministicProbe(t *testing.T) {
	oldProbe := wanProbeCall
	defer func() { wanProbeCall = oldProbe }()
	var calls atomic.Int32
	wanProbeCall = func(url string, _ time.Duration) (bool, error) {
		calls.Add(1)
		if strings.Contains(url, "ok") {
			return true, nil
		}
		return false, errors.New("offline")
	}
	h := newHealthMonitor()
	settings := defaultSettings()
	settings.OpenProbes = []string{"http://bad", "http://ok"}
	settings.InternetCheckTimeoutSec = 1
	h.SetSettingsSource(func() AppSettings { return settings })
	status := h.Check()
	if !status.OK || status.Reachable != 1 || status.Total != 2 || status.LatencyMS < 0 {
		t.Fatalf("status=%+v", status)
	}
	if got := h.Status(); got.CheckedAt != status.CheckedAt {
		t.Fatalf("stored status=%+v", got)
	}
	wanProbeCall = func(string, time.Duration) (bool, error) {
		calls.Add(1)
		return true, nil
	}
	if !WaitForWAN(time.Second) || calls.Load() < 3 {
		t.Fatalf("WaitForWAN calls=%d", calls.Load())
	}
	wanProbeCall = func(string, time.Duration) (bool, error) { return false, errors.New("offline") }
	if WaitForWAN(time.Millisecond) {
		t.Fatal("WaitForWAN unexpectedly succeeded")
	}
}

func TestPingProtocolSortingAndTCPFallback(t *testing.T) {
	cases := []struct {
		server VLESSServer
		want   string
	}{
		{VLESSServer{}, "VLESS plain (tcp)"},
		{VLESSServer{Security: "tls", Network: "ws"}, "VLESS TLS (ws)"},
		{VLESSServer{Security: "reality", Network: "grpc"}, "VLESS Reality (grpc)"},
	}
	for _, test := range cases {
		if got := describeProtocol(&test.server); got != test.want {
			t.Fatalf("describeProtocol=%q want=%q", got, test.want)
		}
	}

	results := []PingResult{
		{ServerID: "bad", LatencyMS: -1},
		{ServerID: "incompat", LatencyMS: -1, Incompat: true},
		{ServerID: "slow", LatencyMS: 50},
		{ServerID: "fast", LatencyMS: 10},
	}
	sortByLatency(results)
	if got := results[0].ServerID; got != "fast" {
		t.Fatalf("sorted first=%s", got)
	}
	if best := fastestReachable(results); best == nil || best.ServerID != "fast" {
		t.Fatalf("best=%+v", best)
	}
	if best := fastestReachable([]PingResult{{LatencyMS: -1}, {LatencyMS: 1, Incompat: true}}); best != nil {
		t.Fatalf("unexpected best=%+v", best)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err == nil {
			conn.Close()
		}
	}()
	addr := ln.Addr().(*net.TCPAddr)
	server := VLESSServer{ID: "tcp", Name: "tcp", Address: "127.0.0.1", Port: addr.Port}
	if result := pingTCPFallback(&server, time.Second); result.LatencyMS < 0 || result.Error != "" {
		t.Fatalf("successful TCP result=%+v", result)
	}
	ln.Close()
	if result := pingTCPFallback(&server, 50*time.Millisecond); result.LatencyMS != -1 || result.Error == "" {
		t.Fatalf("failed TCP result=%+v", result)
	}
	if result := pingViaSOCKS(&server, 1, 20*time.Millisecond, ""); result.LatencyMS != -1 || result.Error == "" {
		t.Fatalf("SOCKS failure result=%+v", result)
	}
}

func TestProcessLoggingBufferAndUtilities(t *testing.T) {
	for value, want := range map[string]serviceLogLevel{
		"error": serviceLogError, "warn": serviceLogWarn, "info": serviceLogInfo,
		"debug": serviceLogDebug, "trace": serviceLogTrace, "other": serviceLogInfo,
	} {
		if got := parseServiceLogLevel(value); got != want {
			t.Fatalf("parse %q=%v want=%v", value, got, want)
		}
	}
	for level, want := range map[serviceLogLevel]string{
		serviceLogError: "ERROR", serviceLogWarn: "WARN", serviceLogInfo: "INFO",
		serviceLogDebug: "DEBUG", serviceLogTrace: "TRACE",
	} {
		if got := serviceLogLevelName(level); got != want {
			t.Fatalf("name %v=%q", level, got)
		}
	}
	if component, message := splitLogComponent("[ping] done"); component != "ping" || message != "done" {
		t.Fatalf("split=%q %q", component, message)
	}
	if component, _ := splitLogComponent("plain"); component != "manager" {
		t.Fatalf("plain component=%q", component)
	}

	rb := newRingBuffer()
	rb.setLevel("trace")
	pipeLog(rb, strings.NewReader("one\ntwo\n"))
	for i := 0; i < logBufSize+5; i++ {
		rb.logEvent(serviceLogInfo, "test", "line", "message", field("i", i), field("", nil))
	}
	lines, seq := rb.Lines(0)
	if len(lines) != logBufSize || seq != logBufSize+7 {
		t.Fatalf("lines=%d seq=%d", len(lines), seq)
	}
	if newer, _ := rb.Lines(seq - 1); len(newer) != 1 {
		t.Fatalf("newer lines=%d", len(newer))
	}
	if none, _ := rb.Lines(seq); len(none) != 0 {
		t.Fatalf("unexpected lines=%v", none)
	}
	if entries, _ := rb.Entries(seq - 2); len(entries) != 2 {
		t.Fatalf("entries=%d", len(entries))
	}

	pm := NewProcessManager(t.TempDir())
	pm.SetServiceLogLevel("debug")
	pm.log(serviceLogInfo, "[manager] hello")
	pm.event(serviceLogWarn, "test", "warn", "warning")
	if pm.nextOperationID("op") != "op-000001" || pm.nextOperationID("op") != "op-000002" {
		t.Fatal("operation sequence is not monotonic")
	}
	if pm.TunRunning() || pm.Status().Running {
		t.Fatal("new process manager must be stopped")
	}
	if err := pm.Stop(); err != nil {
		t.Fatal(err)
	}
	if formatUptime(5) != "5s" || formatUptime(65) != "1m05s" || formatUptime(3665) != "1h01m05s" {
		t.Fatal("unexpected uptime formatting")
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if !waitForPort(ln.Addr().String(), time.Second) {
		t.Fatal("open port was not detected")
	}
	ln.Close()
	if waitForPort(ln.Addr().String(), 20*time.Millisecond) {
		t.Fatal("closed port was reported open")
	}
}

type failingBody struct{}

func (failingBody) Read([]byte) (int, error) { return 0, errors.New("read failed") }
func (failingBody) Close() error             { return nil }

func TestBypassFetchValidation(t *testing.T) {
	domains := make([]string, 110)
	for i := range domains {
		domains[i] = fmt.Sprintf("host-%d.example", i)
	}
	clientFor := func(status int, body io.ReadCloser, err error) *http.Client {
		return &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			if err != nil {
				return nil, err
			}
			return &http.Response{StatusCode: status, Body: body, Header: make(http.Header)}, nil
		})}
	}
	got, err := fetchBypassWhitelist(clientFor(200, io.NopCloser(strings.NewReader(strings.Join(domains, "\n"))), nil))
	if err != nil || len(got) != len(domains) {
		t.Fatalf("domains=%d err=%v", len(got), err)
	}
	tests := []*http.Client{
		clientFor(0, nil, errors.New("dial failed")),
		clientFor(500, io.NopCloser(strings.NewReader("")), nil),
		clientFor(200, io.NopCloser(strings.NewReader("one.example")), nil),
		clientFor(200, failingBody{}, nil),
	}
	for i, client := range tests {
		if _, err := fetchBypassWhitelist(client); err == nil {
			t.Fatalf("case %d unexpectedly succeeded", i)
		}
	}
	if directBypassHTTPClient(time.Second) == nil {
		t.Fatal("direct bypass client is nil")
	}
}

func TestRoutingAndStringHelpers(t *testing.T) {
	AddPingBypass("127.0.0.1")
	ClearPingBypasses()
	if got := ResolveAddrs("127.0.0.1"); len(got) != 1 || got[0] != "127.0.0.1" {
		t.Fatalf("IPv4 resolve=%v", got)
	}
	if got := ResolveAddrs("::1"); got != nil {
		t.Fatalf("IPv6 resolve=%v", got)
	}
	if err := waitForTun(time.Millisecond); err == nil {
		t.Fatal("missing tun unexpectedly found")
	}
	if err := requireRouteCommand("test", "command-that-does-not-exist"); err == nil {
		t.Fatal("missing route command unexpectedly succeeded")
	}
	if orDefault("", "x") != "x" || orDefault("a", "x") != "a" {
		t.Fatal("orDefault failed")
	}
	if camelToSnake("uplinkHTTPMethod") != "uplink_httpmethod" || camelToSnake("SimpleValue") != "simple_value" {
		t.Fatal("camelToSnake failed")
	}
	if got := splitCSV(" h2, ,http/1.1 "); len(got) != 2 || got[0] != "h2" {
		t.Fatalf("splitCSV=%v", got)
	}
}

func TestCancelledPingBatchDoesNotStartProbes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	server := VLESSServer{
		ID:      "cancelled",
		Name:    "cancelled",
		Address: "127.0.0.1",
		Port:    443,
		Network: "tcp",
	}
	completed := 0
	results := pingBatchViaSingBoxContext(ctx, []VLESSServer{server}, time.Second, "", 1, func(int, PingResult) {
		completed++
	})
	if completed != 0 {
		t.Fatalf("cancelled batch completed %d probes", completed)
	}
	if len(results) != 1 || results[0].LatencyMS != -1 {
		t.Fatalf("cancelled batch results = %#v", results)
	}
}
