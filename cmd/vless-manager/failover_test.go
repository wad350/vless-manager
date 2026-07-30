package main

import (
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newFailoverTestController(t *testing.T) (*apiServer, *failoverController) {
	t.Helper()
	dir := t.TempDir()
	cfg := defaultConfig()
	cfg.AutoFailover = true
	cfg.Settings.FailoverHysteresis = 1
	cfg.Settings.FailoverVPNSwapAfterFails = 2
	cfg.Settings.FailoverStartBackoffSec = 10
	cfg.Settings.OpenProbes = []string{"open"}
	cfg.Settings.WhitelistProbes = []string{"white"}
	api := newAPIServer(
		NewProcessManager(dir),
		cfg,
		nil,
		filepath.Join(dir, "config.json"),
		filepath.Join(dir, "subscriptions.json"),
	)
	fc := api.failover
	fc.swapRestartDelay = 0
	return api, fc
}

func probeResults(urls []string, ok bool) ([]ProbeResult, bool) {
	results := make([]ProbeResult, len(urls))
	for i, url := range urls {
		results[i] = ProbeResult{URL: url, OK: ok, LatencyMS: 1}
	}
	return results, ok
}

func TestFailoverOuterDecisions(t *testing.T) {
	tests := []struct {
		name      string
		openOK    bool
		whiteOK   bool
		running   bool
		wantStart int32
		wantStop  int32
		reason    string
	}{
		{"free internet", true, true, false, 0, 0, "Свободный интернет"},
		{"whitelist starts VPN", false, true, false, 1, 0, "Whitelist активен"},
		{"free internet stops VPN", true, true, true, 0, 1, "Свободный интернет"},
		{"loss stops VPN", false, false, true, 0, 1, "Связи нет"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, fc := newFailoverTestController(t)
			fc.runProbesFn = func(urls []string, _ time.Duration) ([]ProbeResult, bool) {
				if urls[0] == "open" {
					return probeResults(urls, test.openOK)
				}
				return probeResults(urls, test.whiteOK)
			}
			fc.statusFn = func() ProcessStatus { return ProcessStatus{Running: test.running} }
			var starts, stops atomic.Int32
			fc.startFn = func() error {
				starts.Add(1)
				return nil
			}
			fc.stopFn = func() error {
				stops.Add(1)
				return nil
			}

			fc.outerTick()
			state := fc.State()
			if starts.Load() != test.wantStart || stops.Load() != test.wantStop {
				t.Fatalf("starts=%d stops=%d state=%+v", starts.Load(), stops.Load(), state)
			}
			if !strings.Contains(state.Reason, test.reason) || state.Pending != 0 {
				t.Fatalf("state=%+v", state)
			}
		})
	}
}

func TestFailoverOuterHysteresisAndStartBackoff(t *testing.T) {
	api, fc := newFailoverTestController(t)
	api.cfg.Settings.FailoverHysteresis = 2
	fc.runProbesFn = func(urls []string, _ time.Duration) ([]ProbeResult, bool) {
		return probeResults(urls, urls[0] == "white")
	}
	fc.statusFn = func() ProcessStatus { return ProcessStatus{} }
	var starts atomic.Int32
	fc.startFn = func() error {
		starts.Add(1)
		return errors.New("start failed")
	}

	fc.outerTick()
	if state := fc.State(); state.Pending != 1 || !strings.Contains(state.Reason, "1/2") {
		t.Fatalf("first state=%+v", state)
	}
	fc.outerTick()
	if starts.Load() != 1 || !strings.Contains(fc.State().Reason, "Запуск не удался") {
		t.Fatalf("starts=%d state=%+v", starts.Load(), fc.State())
	}
	fc.outerTick()
	fc.outerTick()
	if starts.Load() != 1 || !strings.Contains(fc.State().Reason, "backoff") {
		t.Fatalf("backoff starts=%d state=%+v", starts.Load(), fc.State())
	}
	fc.ResetBackoff()
	fc.outerTick()
	fc.outerTick()
	if starts.Load() != 2 {
		t.Fatalf("starts after reset=%d", starts.Load())
	}
}

func TestFailoverProbesWhenOperatorAutomationDisabled(t *testing.T) {
	_, fc := newFailoverTestController(t)
	fc.enabled = false
	fc.state.Enabled = false
	fc.runProbesFn = func(urls []string, _ time.Duration) ([]ProbeResult, bool) {
		return probeResults(urls, urls[0] == "open")
	}
	fc.statusFn = func() ProcessStatus { return ProcessStatus{Running: true} }
	var starts, stops atomic.Int32
	fc.startFn = func() error {
		starts.Add(1)
		return nil
	}
	fc.stopFn = func() error {
		stops.Add(1)
		return nil
	}

	fc.outerTick()

	state := fc.State()
	if len(state.OpenProbes) == 0 || len(state.WhitelistProbes) == 0 || state.LastCheck.IsZero() {
		t.Fatalf("disabled policy did not refresh probe state: %+v", state)
	}
	if !state.OpenOK || state.WhitelistOK {
		t.Fatalf("unexpected probe aggregation: %+v", state)
	}
	if starts.Load() != 0 || stops.Load() != 0 {
		t.Fatalf("disabled policy changed VPN: starts=%d stops=%d", starts.Load(), stops.Load())
	}
	if !strings.Contains(state.Reason, "автоуправление выключено") || state.Pending != 0 {
		t.Fatalf("unexpected disabled-policy state: %+v", state)
	}
}

func TestFailoverHealthDecisions(t *testing.T) {
	api, fc := newFailoverTestController(t)
	api.cfg.Settings.FailoverHealthURL = "http://health.example/check"
	fc.statusFn = func() ProcessStatus { return ProcessStatus{} }
	fc.healthTick()
	if state := fc.State(); state.VPNHealthOK || state.VPNHealthFails != 0 {
		t.Fatalf("stopped state=%+v", state)
	}

	fc.statusFn = func() ProcessStatus { return ProcessStatus{Running: true} }
	var probedURL string
	fc.vpnProbeFn = func(url string, _ time.Duration) (bool, error) {
		probedURL = url
		return true, nil
	}
	fc.healthTick()
	if state := fc.State(); !state.VPNHealthOK || state.VPNHealthFails != 0 ||
		state.VPNHealthLatencyMS < 0 ||
		state.VPNHealthURL != "http://health.example/check" ||
		state.VPNHealthFailLimit != api.cfg.Settings.FailoverVPNSwapAfterFails ||
		probedURL != "http://health.example/check" {
		t.Fatalf("healthy state=%+v", state)
	}

	fc.vpnProbeFn = func(string, time.Duration) (bool, error) {
		return false, errors.New("probe failed")
	}
	fc.runProbesFn = func(urls []string, _ time.Duration) ([]ProbeResult, bool) {
		return probeResults(urls, false)
	}
	fc.healthTick()
	fc.healthTick()
	if state := fc.State(); state.VPNHealthFails != 2 || state.VPNHealthLatencyMS != -1 {
		t.Fatalf("no-whitelist state=%+v", state)
	}

	fc.mu.Lock()
	fc.state.VPNHealthFails = 0
	fc.mu.Unlock()
	fc.runProbesFn = func(urls []string, _ time.Duration) ([]ProbeResult, bool) {
		return probeResults(urls, true)
	}
	fc.chooseFn = func() string { return "" }
	fc.healthTick()
	fc.healthTick()
	if state := fc.State(); state.VPNHealthFails != 0 {
		t.Fatalf("no candidate state=%+v", state)
	}

	var stops, starts atomic.Int32
	fc.nextSwapAttempt = time.Time{}
	fc.chooseFn = func() string { return "replacement" }
	fc.stopFn = func() error {
		stops.Add(1)
		return nil
	}
	fc.startSelectedFn = func(*Config) error {
		starts.Add(1)
		return nil
	}
	fc.healthTick()
	fc.healthTick()
	if stops.Load() != 1 || starts.Load() != 1 || fc.State().VPNHealthFails != 0 {
		t.Fatalf("swap stops=%d starts=%d state=%+v", stops.Load(), starts.Load(), fc.State())
	}
}

func TestFailoverLifecycleAndProbeAggregation(t *testing.T) {
	oldProbe := wanProbeCall
	defer func() { wanProbeCall = oldProbe }()
	wanProbeCall = func(url string, _ time.Duration) (bool, error) {
		if url == "ok" {
			return true, nil
		}
		return false, errors.New("offline")
	}
	results, ok := runProbes([]string{"bad", "ok"}, time.Second)
	if !ok || len(results) != 2 || results[0].LatencyMS != -1 || !results[1].OK {
		t.Fatalf("results=%+v ok=%v", results, ok)
	}

	_, fc := newFailoverTestController(t)
	if !fc.Enabled() {
		t.Fatal("controller must start enabled")
	}
	fc.runProbesFn = func(urls []string, _ time.Duration) ([]ProbeResult, bool) {
		return probeResults(urls, true)
	}
	fc.statusFn = func() ProcessStatus { return ProcessStatus{} }
	done := make(chan struct{})
	go func() {
		fc.Run()
		close(done)
	}()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		fc.mu.Lock()
		running := fc.running
		fc.mu.Unlock()
		if running {
			break
		}
		time.Sleep(time.Millisecond)
	}
	fc.Run() // idempotent second call
	fc.ReloadSettings()
	fc.ReloadSettings() // coalesced and non-blocking
	fc.Stop()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("controller did not stop")
	}
	fc.SetEnabled(false)
	if fc.Enabled() || fc.State().Reason != "Автоуправление VPN выключено; проверки продолжаются" {
		t.Fatalf("disabled state=%+v", fc.State())
	}
}

func TestTunnelHealthIsIndependentFromOperatorAutomation(t *testing.T) {
	_, fc := newFailoverTestController(t)
	fc.SetEnabled(false)
	fc.statusFn = func() ProcessStatus { return ProcessStatus{Running: true} }
	fc.vpnProbeFn = func(string, time.Duration) (bool, error) { return true, nil }

	fc.healthTick()
	state := fc.State()
	if !state.VPNHealthOK || state.VPNHealthCheck.IsZero() {
		t.Fatalf("manual VPN was not checked with operator automation off: %+v", state)
	}

	fc.SetTunnelFailoverEnabled(false)
	fc.vpnProbeFn = func(string, time.Duration) (bool, error) {
		t.Fatal("disabled tunnel monitoring must not probe")
		return false, nil
	}
	fc.healthTick()
	state = fc.State()
	if state.TunnelFailoverEnabled || state.VPNHealthOK || !state.VPNHealthCheck.IsZero() ||
		state.VPNHealthLatencyMS != -1 {
		t.Fatalf("disabled tunnel monitoring state=%+v", state)
	}
}
