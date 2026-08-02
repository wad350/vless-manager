package main

import (
	"fmt"
	"net/http"
	"sync"
	"time"

	"golang.org/x/net/proxy"
)

// Auto-failover controller. Decides VPN on/off based on probes that always
// bypass the tunnel (WAN-fwmark dialer). When VPN is on, also probes the VPN
// itself; after a sustained failure swaps to a different server.
//
// Every tunable on this path lives in AppSettings (see settings.go); the
// controller re-reads them through settingsSnapshot() each tick so a UI
// settings PATCH takes effect without restarting the process.

// FailoverState is exposed to the UI via /api/failover and /api/status.
type FailoverState struct {
	Enabled               bool          `json:"enabled"` // automatic VPN on/off policy
	TunnelFailoverEnabled bool          `json:"tunnel_failover_enabled"`
	VPNOn                 bool          `json:"vpn_on"`
	OpenProbes            []ProbeResult `json:"open_probes"`
	WhitelistProbes       []ProbeResult `json:"whitelist_probes"`
	OpenOK                bool          `json:"open_ok"`
	WhitelistOK           bool          `json:"whitelist_ok"`
	VPNHealthOK           bool          `json:"vpn_health_ok"`    // last probe through VPN
	VPNHealthCheck        time.Time     `json:"vpn_health_check"` // last VPN probe time
	VPNHealthLatencyMS    int64         `json:"vpn_health_latency_ms"`
	VPNHealthFails        int           `json:"vpn_health_fails"` // consecutive VPN failures
	VPNHealthURL          string        `json:"vpn_health_url"`   // URL used for VPN probe
	VPNHealthFailLimit    int           `json:"vpn_health_fail_limit"`
	Reason                string        `json:"reason"`
	LastCheck             time.Time     `json:"last_check"`
	Pending               int           `json:"pending"` // hysteresis counter
}

type failoverController struct {
	mu            sync.Mutex
	outerMu       sync.Mutex
	healthMu      sync.Mutex
	enabled       bool
	tunnelEnabled bool
	running       bool // true while the Run goroutine is live
	state         FailoverState
	api           *apiServer

	proposedOn bool
	pending    int

	// nextStartAttempt — earliest time outerTick is allowed to call
	// startVPNInternal again. Set after a failed attempt; prevents the
	// 30 s tick from re-triggering ping batch in a hot loop.
	nextStartAttempt time.Time

	// nextSwapAttempt — earliest time healthTick is allowed to call
	// chooseAlternativeServer again.
	nextSwapAttempt time.Time

	stopCh   chan struct{}
	reloadCh chan struct{}

	runProbesFn      func([]string, time.Duration) ([]ProbeResult, bool)
	vpnProbeFn       func(string, time.Duration) (bool, error)
	statusFn         func() ProcessStatus
	startFn          func() error
	stopFn           func() error
	chooseFn         func() string
	startSelectedFn  func(*Config) error
	swapRestartDelay time.Duration
}

func newFailoverController(api *apiServer, enabled, tunnelEnabled bool) *failoverController {
	reason := "Ожидание первой проверки оператора"
	if !enabled {
		reason = "Автоуправление VPN выключено; проверки продолжаются"
	}
	settings := api.settingsSnapshot()
	fc := &failoverController{
		api:           api,
		enabled:       enabled,
		tunnelEnabled: tunnelEnabled,
		state: FailoverState{
			Enabled:               enabled,
			TunnelFailoverEnabled: tunnelEnabled,
			VPNHealthLatencyMS:    -1,
			VPNHealthFailLimit:    settings.FailoverVPNSwapAfterFails,
			Reason:                reason,
		},
		stopCh:   make(chan struct{}),
		reloadCh: make(chan struct{}, 1),
	}
	fc.runProbesFn = runProbes
	fc.vpnProbeFn = vpnProbe
	fc.statusFn = api.pm.Status
	fc.startFn = api.startVPNInternal
	fc.stopFn = api.pm.Stop
	fc.chooseFn = api.chooseAlternativeServer
	fc.startSelectedFn = api.startManagedVPN
	fc.swapRestartDelay = 500 * time.Millisecond
	return fc
}

// ReloadSettings applies changed intervals immediately instead of waiting for
// the previous ticker period to elapse.
func (fc *failoverController) ReloadSettings() {
	select {
	case fc.reloadCh <- struct{}{}:
	default:
	}
}

func (fc *failoverController) Enabled() bool {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	return fc.enabled
}

func (fc *failoverController) SetEnabled(on bool) {
	fc.mu.Lock()
	fc.enabled = on
	fc.state.Enabled = on
	launch := false
	checkNow := false
	if !on {
		fc.state.Reason = "Автоуправление VPN выключено; проверки продолжаются"
		checkNow = fc.running
	} else if !fc.running {
		// Reset stopCh in case Stop() was called previously, then launch.
		fc.stopCh = make(chan struct{})
		launch = true
	} else {
		checkNow = true
	}
	fc.mu.Unlock()
	if launch {
		go fc.Run()
	} else if checkNow {
		go fc.outerTick()
	}
}

func (fc *failoverController) TunnelFailoverEnabled() bool {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	return fc.tunnelEnabled
}

func (fc *failoverController) SetTunnelFailoverEnabled(on bool) {
	fc.mu.Lock()
	fc.tunnelEnabled = on
	fc.state.TunnelFailoverEnabled = on
	launch := !fc.running
	if !on {
		fc.state.VPNHealthOK = false
		fc.state.VPNHealthFails = 0
		fc.state.VPNHealthCheck = time.Time{}
		fc.state.VPNHealthLatencyMS = -1
		fc.state.VPNHealthURL = ""
	}
	if launch {
		fc.stopCh = make(chan struct{})
	}
	fc.mu.Unlock()
	if launch {
		go fc.Run()
	} else if on {
		fc.CheckTunnelNow()
	}
}

// CheckTunnelNow schedules an immediate health-check. It is used after a
// manual start and when tunnel monitoring is enabled; healthMu serializes it
// with the periodic ticker.
func (fc *failoverController) CheckTunnelNow() {
	go fc.healthTick()
}

func (fc *failoverController) State() FailoverState {
	fc.mu.Lock()
	state := fc.state
	fc.mu.Unlock()
	// Process state is authoritative. In particular, a manual start/stop must
	// be visible without waiting for either automation ticker.
	state.VPNOn = fc.statusFn().Running
	return state
}

// ResetBackoff clears the start/swap cooldowns so the next outer/health tick
// retries immediately. Called when the user manually triggers /api/start —
// don't make them wait 5 min after fixing the upstream issue.
func (fc *failoverController) ResetBackoff() {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	fc.nextStartAttempt = time.Time{}
	fc.nextSwapAttempt = time.Time{}
}

// Run launches the two probe cadences and blocks. Tick intervals are read
// from AppSettings and re-applied on every interval change — so a settings
// PATCH from the UI is honoured without restart.
// Idempotent: a second concurrent call returns immediately.
func (fc *failoverController) Run() {
	fc.mu.Lock()
	if fc.running {
		fc.mu.Unlock()
		return
	}
	fc.running = true
	stopCh := fc.stopCh
	fc.mu.Unlock()
	defer func() {
		fc.mu.Lock()
		fc.running = false
		if fc.stopCh == stopCh {
			fc.stopCh = make(chan struct{})
		}
		fc.mu.Unlock()
	}()

	st := fc.api.settingsSnapshot()
	curOuter := st.OuterInterval()
	curHealth := st.HealthInterval()
	fc.api.pm.event(serviceLogInfo, "failover", "controller.started",
		"контроллер автоматического переключения запущен",
		field("outer_interval_ms", curOuter.Milliseconds()),
		field("health_interval_ms", curHealth.Milliseconds()),
		field("hysteresis", st.FailoverHysteresis),
		field("swap_after_fails", st.FailoverVPNSwapAfterFails))
	fc.outerTick()  // initial operator-policy decision at boot
	fc.healthTick() // independently inspect a VPN that was already running

	outer := time.NewTicker(curOuter)
	health := time.NewTicker(curHealth)
	defer outer.Stop()
	defer health.Stop()
	for {
		select {
		case <-stopCh:
			return
		case <-fc.reloadCh:
			st = fc.api.settingsSnapshot()
			if d := st.OuterInterval(); d > 0 {
				outer.Reset(d)
				curOuter = d
			}
			if d := st.HealthInterval(); d > 0 {
				health.Reset(d)
				curHealth = d
			}
			fc.api.pm.event(serviceLogDebug, "failover", "controller.reloaded",
				"таймеры автоматического переключения обновлены",
				field("outer_interval_ms", curOuter.Milliseconds()),
				field("health_interval_ms", curHealth.Milliseconds()))
			continue
		case <-outer.C:
			fc.outerTick()
		case <-health.C:
			fc.healthTick()
		}
		// Re-evaluate intervals after each tick so a settings PATCH lands
		// within at most one full tick of its previous value.
		st = fc.api.settingsSnapshot()
		if d := st.OuterInterval(); d != curOuter && d > 0 {
			outer.Reset(d)
			curOuter = d
		}
		if d := st.HealthInterval(); d != curHealth && d > 0 {
			health.Reset(d)
			curHealth = d
		}
	}
}

func (fc *failoverController) replaceStoppedTunnel(st AppSettings, opID, trigger string) {
	fc.mu.Lock()
	fc.nextSwapAttempt = time.Now().Add(st.StartBackoff())
	fc.mu.Unlock()

	newName := fc.chooseFn()
	if newName == "" {
		fc.api.pm.event(serviceLogWarn, "failover", "swap.no_candidate",
			"рабочий альтернативный сервер не найден; VPN оставлен выключенным",
			field("op_id", opID),
			field("trigger", trigger))
		fc.mu.Lock()
		fc.state.VPNHealthFails = 0
		fc.mu.Unlock()
		return
	}
	time.Sleep(fc.swapRestartDelay)
	fc.api.mu.RLock()
	cfgSnap := cloneConfig(fc.api.cfg)
	fc.api.mu.RUnlock()
	if err := fc.startSelectedFn(cfgSnap); err != nil {
		fc.api.pm.event(serviceLogError, "failover", "swap.failed",
			"VPN не запустился на выбранном сервере",
			field("op_id", opID),
			field("server", newName),
			field("trigger", trigger),
			field("error", err))
	} else {
		fc.api.pm.event(serviceLogInfo, "failover", "swap.succeeded",
			"VPN переключён на новый сервер",
			field("op_id", opID),
			field("server", newName),
			field("trigger", trigger))
	}
	fc.mu.Lock()
	fc.state.VPNHealthFails = 0
	fc.mu.Unlock()
}

func (fc *failoverController) Stop() {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	if fc.running {
		close(fc.stopCh)
	}
}

// runProbes probes every URL in parallel and returns per-URL results +
// whether at least one succeeded. Per-probe timeout is supplied by the caller
// so it can be wired to AppSettings.FailoverProbeTimeoutSec.
func runProbes(urls []string, timeout time.Duration) ([]ProbeResult, bool) {
	results := make([]ProbeResult, len(urls))
	var wg sync.WaitGroup
	for i, u := range urls {
		wg.Add(1)
		go func(i int, u string) {
			defer wg.Done()
			start := time.Now()
			ok, err := wanProbeCall(u, timeout)
			r := ProbeResult{URL: u, OK: ok, LatencyMS: time.Since(start).Milliseconds()}
			if err != nil {
				r.Error = err.Error()
				r.LatencyMS = -1
			}
			results[i] = r
		}(i, u)
	}
	wg.Wait()
	anyOK := false
	for _, r := range results {
		if r.OK {
			anyOK = true
			break
		}
	}
	return results, anyOK
}

// outerTick always probes open and whitelist sites outside the VPN. The
// operator policy switch controls only whether the resulting decision may
// start or stop VPN.
func (fc *failoverController) outerTick() {
	fc.outerMu.Lock()
	defer fc.outerMu.Unlock()

	opID := fc.api.pm.nextOperationID("outer")
	started := time.Now()
	st := fc.api.settingsSnapshot()
	openR, openOK := fc.runProbesFn(st.OpenProbes, st.ProbeTimeout())
	whR, whOK := fc.runProbesFn(st.WhitelistProbes, st.ProbeTimeout())

	var (
		wantOn bool
		reason string
	)
	switch {
	case !openOK && !whOK:
		wantOn = false
		reason = "Связи нет — VPN должен быть выключен"
	case openOK:
		wantOn = false
		reason = "Свободный интернет — VPN не нужен"
	case !openOK && whOK:
		wantOn = true
		reason = "Whitelist активен — VPN нужен"
	}

	running := fc.statusFn().Running
	policyEnabled := fc.Enabled()
	fc.api.pm.event(serviceLogDebug, "failover", "outer.completed",
		"проверка доступа через WAN завершена",
		field("op_id", opID),
		field("open_ok", openOK),
		field("whitelist_ok", whOK),
		field("vpn_running", running),
		field("operator_control_enabled", policyEnabled),
		field("decision_required", policyEnabled),
		field("desired_vpn", wantOn),
		field("decision", reason),
		field("duration_ms", time.Since(started).Milliseconds()))
	for _, result := range append(openR, whR...) {
		level := serviceLogTrace
		message := "контрольный адрес доступен"
		if !result.OK {
			message = "контрольный адрес недоступен"
		}
		fc.api.pm.event(level, "failover", "outer.probe",
			message,
			field("op_id", opID),
			field("url", result.URL),
			field("ok", result.OK),
			field("latency_ms", result.LatencyMS),
			field("error", result.Error))
	}

	fc.mu.Lock()
	fc.state.OpenProbes = openR
	fc.state.WhitelistProbes = whR
	fc.state.OpenOK = openOK
	fc.state.WhitelistOK = whOK
	fc.state.LastCheck = time.Now()
	fc.state.VPNOn = running

	// The switch may have changed while probes were in flight. Disabled
	// policy still publishes diagnostics but never changes VPN state.
	if !fc.enabled {
		fc.state.Reason = reason + "; автоуправление выключено, VPN не изменяется"
		fc.pending = 0
		fc.state.Pending = 0
		fc.mu.Unlock()
		return
	}

	if wantOn == running {
		fc.state.Reason = reason
		fc.pending = 0
		fc.state.Pending = 0
		fc.mu.Unlock()
		return
	}

	if fc.proposedOn == wantOn {
		fc.pending++
	} else {
		fc.proposedOn = wantOn
		fc.pending = 1
	}
	fc.state.Pending = fc.pending

	if fc.pending < st.FailoverHysteresis {
		fc.state.Reason = fmt.Sprintf("%s (подтверждение %d/%d)", reason, fc.pending, st.FailoverHysteresis)
		fc.mu.Unlock()
		return
	}

	// Backoff after a recent failed startup: avoid hot-looping the
	// ping-batch on a dead provider.
	if wantOn {
		if time.Now().Before(fc.nextStartAttempt) {
			remaining := time.Until(fc.nextStartAttempt).Truncate(time.Second)
			fc.state.Reason = fmt.Sprintf("%s, но в backoff после неудачи (%s осталось)", reason, remaining)
			fc.pending = 0
			fc.state.Pending = 0
			fc.mu.Unlock()
			return
		}
	}

	fc.state.Reason = reason + " → переключаю"
	fc.pending = 0
	fc.state.Pending = 0
	fc.mu.Unlock()

	fc.api.pm.event(serviceLogInfo, "failover", "decision.apply",
		"изменение состояния VPN подтверждено",
		field("op_id", opID),
		field("vpn_on", wantOn),
		field("reason", reason))
	if wantOn {
		if err := fc.startFn(); err != nil {
			fc.mu.Lock()
			fc.nextStartAttempt = time.Now().Add(st.StartBackoff())
			fc.state.Reason = fmt.Sprintf("Запуск не удался: %v. Жду %s до новой попытки", err, st.StartBackoff())
			fc.state.VPNOn = false
			fc.mu.Unlock()
			fc.api.pm.event(serviceLogError, "failover", "vpn.start_failed",
				"VPN не запущен, включена пауза перед повтором",
				field("op_id", opID),
				field("error", err),
				field("backoff_ms", st.StartBackoff().Milliseconds()))
		} else {
			fc.mu.Lock()
			fc.state.VPNOn = true
			fc.state.Reason = reason
			fc.mu.Unlock()
		}
	} else {
		if err := fc.stopFn(); err != nil {
			fc.api.pm.event(serviceLogError, "failover", "vpn.stop_failed",
				"автоматическое отключение VPN завершилось ошибкой",
				field("op_id", opID),
				field("error", err))
		}
		fc.mu.Lock()
		fc.state.VPNOn = false
		fc.state.Reason = reason
		fc.mu.Unlock()
	}
}

// healthTick: probe a real internet endpoint THROUGH the running VPN. If it
// fails 5 times in a row AND whitelist is still reachable, swap to another
// server. Skipped if VPN isn't running.
func (fc *failoverController) healthTick() {
	fc.healthMu.Lock()
	defer fc.healthMu.Unlock()
	if !fc.TunnelFailoverEnabled() {
		return
	}
	if !fc.statusFn().Running {
		fc.mu.Lock()
		fc.state.VPNOn = false
		fc.state.VPNHealthOK = false
		fc.state.VPNHealthFails = 0
		fc.state.VPNHealthCheck = time.Time{}
		fc.state.VPNHealthLatencyMS = -1
		fc.state.VPNHealthURL = ""
		fc.mu.Unlock()
		return
	}

	st := fc.api.settingsSnapshot()
	url := st.FailoverHealthURL
	opID := fc.api.pm.nextOperationID("health")
	started := time.Now()
	ok, probeErr := fc.vpnProbeFn(url, st.HealthTimeout())
	latencyMS := time.Since(started).Milliseconds()

	fc.mu.Lock()
	fc.state.VPNOn = true
	fc.state.VPNHealthOK = ok
	fc.state.VPNHealthCheck = time.Now()
	fc.state.VPNHealthLatencyMS = -1
	fc.state.VPNHealthURL = url
	fc.state.VPNHealthFailLimit = st.FailoverVPNSwapAfterFails
	if ok {
		fc.state.VPNHealthLatencyMS = latencyMS
		fc.state.VPNHealthFails = 0
		fc.mu.Unlock()
		fc.api.pm.event(serviceLogDebug, "failover", "health.succeeded",
			"активный VPN передаёт трафик",
			field("op_id", opID),
			field("url", url),
			field("duration_ms", latencyMS))
		return
	}
	fc.state.VPNHealthFails++
	fails := fc.state.VPNHealthFails
	fc.mu.Unlock()
	failureLevel := serviceLogDebug
	if fails >= st.FailoverVPNSwapAfterFails {
		failureLevel = serviceLogWarn
	}
	fc.api.pm.event(failureLevel, "failover", "health.failed",
		"проверка активного VPN завершилась ошибкой",
		field("op_id", opID),
		field("url", url),
		field("consecutive_fails", fails),
		field("swap_after_fails", st.FailoverVPNSwapAfterFails),
		field("duration_ms", latencyMS),
		field("error", probeErr))

	if fails < st.FailoverVPNSwapAfterFails {
		return
	}
	// Confirm the operator underlay independently. Outer probes may be absent
	// or stale when automatic VPN on/off is disabled.
	whR, whitelistOK := fc.runProbesFn(st.WhitelistProbes, st.ProbeTimeout())
	fc.mu.Lock()
	fc.state.WhitelistProbes = whR
	fc.state.WhitelistOK = whitelistOK
	fc.mu.Unlock()
	if !whitelistOK {
		fc.api.pm.event(serviceLogWarn, "failover", "swap.underlay_unavailable",
			"замена сервера пропущена: базовая сеть оператора недоступна",
			field("op_id", opID))
		return
	}

	// Swap backoff — chooseAlternativeServer runs another ping batch, so
	// don't churn it more than once per backoff window.
	fc.mu.Lock()
	if time.Now().Before(fc.nextSwapAttempt) {
		fc.state.VPNHealthFails = 0
		fc.mu.Unlock()
		return
	}
	fc.nextSwapAttempt = time.Now().Add(st.StartBackoff())
	fc.mu.Unlock()

	swapOpID := fc.api.pm.nextOperationID("swap")
	fc.api.pm.event(serviceLogWarn, "failover", "swap.started",
		"активный VPN не прошёл проверки, выполняется поиск другого сервера",
		field("op_id", swapOpID),
		field("consecutive_fails", fails),
		field("health_interval_ms", st.HealthInterval().Milliseconds()))
	if err := fc.stopFn(); err != nil {
		fc.api.pm.event(serviceLogWarn, "failover", "swap.stop_failed",
			"старый экземпляр sing-box остановлен с ошибкой",
			field("op_id", swapOpID),
			field("error", err))
	}
	fc.replaceStoppedTunnel(st, swapOpID, "VPN-health не пройден")
}

// vpnProbe fetches url via the SOCKS5 health inbound (localhost:socksHealthPort)
// that sing-box exposes alongside the redirect inbound. This verifies that the
// VLESS tunnel is actually working, independent of ip-rule routing.
func vpnProbe(url string, timeout time.Duration) (bool, error) {
	dialer, err := proxy.SOCKS5("tcp",
		fmt.Sprintf("127.0.0.1:%d", socksHealthPort), nil, proxy.Direct)
	if err != nil {
		return false, err
	}
	client := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Dial:              dialer.Dial,
			DisableKeepAlives: true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	const attempts = 3
	var wg sync.WaitGroup
	results := make(chan error, attempts)
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := client.Get(url)
			if err == nil {
				if resp.StatusCode >= 500 {
					err = fmt.Errorf("HTTP %d", resp.StatusCode)
				}
				resp.Body.Close()
			}
			results <- err
		}()
	}
	wg.Wait()
	close(results)

	successes := 0
	var lastErr error
	for err := range results {
		if err == nil {
			successes++
		} else {
			lastErr = err
		}
	}
	if successes >= 2 {
		return true, nil
	}
	return false, fmt.Errorf("VPN health: %d/%d requests succeeded: %w", successes, attempts, lastErr)
}
