package main

import (
	"encoding/json"
	"net/http"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestInternetStatusUsesOperatorSnapshot(t *testing.T) {
	checked := time.Unix(12345, 0)
	tests := []struct {
		name      string
		state     FailoverState
		reachable int
		latency   int64
	}{
		{name: "not checked", latency: -1},
		{name: "whitelist is not open internet", state: FailoverState{
			LastCheck: checked, WhitelistOK: true,
			WhitelistProbes: []ProbeResult{{OK: true}},
			OpenProbes:      []ProbeResult{{URL: "open", LatencyMS: -1, Error: "offline"}},
		}, latency: -1},
		{name: "best successful latency", state: FailoverState{
			LastCheck: checked, OpenOK: true,
			OpenProbes: []ProbeResult{{OK: true, LatencyMS: 70}, {LatencyMS: -1}, {OK: true, LatencyMS: 0}},
		}, reachable: 2, latency: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			status := test.state.InternetStatus()
			if status.OK != (test.reachable > 0) || status.Reachable != test.reachable ||
				status.Total != len(test.state.OpenProbes) || status.LatencyMS != test.latency ||
				status.CheckedAt != test.state.LastCheck || !reflect.DeepEqual(status.Probes, test.state.OpenProbes) {
				t.Fatalf("status=%+v, source=%+v", status, test.state)
			}
		})
	}
}

func TestStatusReusesOperatorProbesWithoutNetworkRequests(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "manual mode", true: "automatic mode"}[enabled], func(t *testing.T) {
			api, fc := newFailoverTestController(t)
			fc.enabled, fc.state.Enabled = enabled, enabled
			fc.statusFn = func() ProcessStatus { return ProcessStatus{} }
			calls := 0
			fc.runProbesFn = func(urls []string, _ time.Duration) ([]ProbeResult, bool) {
				calls++
				return probeResults(urls, true)
			}
			fc.outerTick()
			checked := fc.State().LastCheck
			for i := 0; i < 3; i++ {
				rec := apiRequest(t, api, http.MethodGet, "/api/status", "")
				var got struct {
					Internet InternetStatus `json:"internet"`
					Failover FailoverState  `json:"failover"`
				}
				if rec.Code != http.StatusOK {
					t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				if !got.Internet.OK || got.Internet.Reachable != 1 || !got.Internet.CheckedAt.Equal(checked) ||
					!reflect.DeepEqual(got.Internet, got.Failover.InternetStatus()) {
					t.Fatalf("status uses a different operator snapshot: %+v", got)
				}
			}
			if calls != 2 {
				t.Fatalf("probe groups=%d, want one open and one whitelist sweep", calls)
			}
		})
	}
}

func TestManualInternetCheckUpdatesSharedStatusWithoutChangingVPNPolicy(t *testing.T) {
	api, fc := newFailoverTestController(t)
	api.cfg.Settings.FailoverProbeTimeoutSec = 7
	api.cfg.Settings.FailoverHysteresis = 2
	fc.statusFn = func() ProcessStatus { return ProcessStatus{Running: true} }
	fc.pending, fc.state.Pending, fc.proposedOn = 1, 1, false
	var starts, stops int
	fc.startFn = func() error { starts++; return nil }
	fc.stopFn = func() error { stops++; return nil }
	var calls int
	fc.runProbesFn = func(urls []string, timeout time.Duration) ([]ProbeResult, bool) {
		calls++
		if timeout != 7*time.Second {
			t.Fatalf("manual timeout=%s, want operator timeout", timeout)
		}
		return probeResults(urls, true)
	}
	rec := apiRequest(t, api, http.MethodPost, "/api/internet/check", "")
	var got InternetStatus
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	state := fc.State()
	if !got.OK || got.CheckedAt.IsZero() || !got.CheckedAt.Equal(state.LastCheck) ||
		!state.OpenOK || !state.WhitelistOK || calls != 2 {
		t.Fatalf("manual response=%+v state=%+v calls=%d", got, state, calls)
	}
	if starts != 0 || stops != 0 || fc.pending != 1 || state.Pending != 1 || fc.proposedOn {
		t.Fatalf("manual check affected VPN policy: starts=%d stops=%d state=%+v", starts, stops, state)
	}
	fc.outerTick()
	if stops != 1 {
		t.Fatalf("automatic decision no longer works after manual check: stops=%d", stops)
	}
}

func TestManualAndScheduledWANChecksDoNotOverlap(t *testing.T) {
	_, fc := newFailoverTestController(t)
	fc.enabled = false
	var active, peak atomic.Int32
	fc.runProbesFn = func(urls []string, _ time.Duration) ([]ProbeResult, bool) {
		n := active.Add(1)
		for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
		}
		time.Sleep(time.Millisecond)
		active.Add(-1)
		return probeResults(urls, true)
	}
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); fc.outerTick() }()
		go func() { defer wg.Done(); fc.CheckInternet() }()
	}
	wg.Wait()
	if peak.Load() != 1 || active.Load() != 0 {
		t.Fatalf("WAN checks overlap: peak=%d active=%d", peak.Load(), active.Load())
	}
}
