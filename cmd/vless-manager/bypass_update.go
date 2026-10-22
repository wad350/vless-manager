package main

import (
	"context"
	"fmt"
	"slices"
	"time"
)

func (s *apiServer) fetchBypassSource(ctx context.Context, source string) ([]string, string, error) {
	timeout := s.settingsSnapshot().SubscriptionFetchTimeout()
	client := directBypassHTTPClient(timeout)
	domains, err := fetchBypassDomainSource(ctx, client, source)
	client.CloseIdleConnections()
	if err == nil || ctx.Err() != nil {
		return domains, "wan", err
	}
	// Reuse the running core instead of allocating another Xray on a MIPS
	// router. This dialer explicitly forces the tunnel even for bypass hosts.
	if tunnelClient, tunnelErr := s.pm.tunnelSpeedTestClient(1); tunnelErr == nil {
		tunnelClient.Timeout = timeout
		domains, err = fetchBypassDomainSource(ctx, tunnelClient, source)
		tunnelClient.CloseIdleConnections()
		return domains, "vless", err
	}
	s.mu.RLock()
	active := s.cfg.activeServer()
	var copy *VLESSServer
	if active != nil {
		cloned := cloneVLESSServer(*active)
		copy = &cloned
	}
	s.mu.RUnlock()
	if copy == nil {
		return nil, "wan", err
	}
	s.pm.event(serviceLogDebug, "bypass", "fetch.retry", "загружаем список через VLESS", field("source", source), field("error", err))
	st := s.settingsSnapshot()
	client, box, err := httpClientViaVLESS(copy, timeout, st.PingStartupSleep(), st.PingTestURL, s.pm.logs)
	if err != nil {
		return nil, "vless", err
	}
	defer box.Close()
	defer client.CloseIdleConnections()
	domains, err = fetchBypassDomainSource(ctx, client, source)
	return domains, "vless", err
}

func (s *apiServer) refreshBypassLists(ctx context.Context, source string) (bypassStatus, error) {
	lease, err := s.operations.Acquire(ctx, operationRequest{
		Kind: "bypass", Title: "Обновление белого списка", Source: source,
		Cancellable: true, StallLimit: 90 * time.Second,
	})
	if err != nil {
		return bypassStatus{}, err
	}
	var operationErr error
	defer func() { lease.Finish(operationErr) }()
	ctx = lease.Context()
	started := time.Now()
	s.mu.Lock()
	s.bypassDiag = bypassDiagnostics{OperationID: s.pm.nextOperationID("bypass"), AttemptAt: started}
	s.mu.Unlock()
	fail := func(err error) (bypassStatus, error) {
		operationErr = err
		s.mu.Lock()
		s.bypassDiag.Error = err.Error()
		s.bypassDiag.DurationMS = time.Since(started).Milliseconds()
		s.mu.Unlock()
		s.pm.event(serviceLogError, "bypass", "refresh.failed", "обновление белого списка не завершено", field("error", err))
		return bypassStatus{}, err
	}
	lease.Progress(operationProgress{Total: 2, Message: "Загружаем основной список hxehex"})
	base, transport, err := s.bypassFetch(ctx, bypassWhitelistURL)
	if err != nil {
		return fail(err)
	}
	lease.Progress(operationProgress{Done: 1, Total: 2, Message: "Обновляем сохранённый список"})
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	s.mu.Lock()
	previous := s.cfg.BypassCache
	cache := previous
	cache.Domains = base
	cache.Source = bypassWhitelistURL
	changed := !slices.Equal(previous.Domains, cache.Domains) || previous.Source != cache.Source
	if changed {
		cache.UpdatedAt = time.Now()
	}
	s.cfg.BypassCache = cache
	if changed {
		if err := saveConfig(s.cfgPath, s.cfg); err != nil {
			s.cfg.BypassCache = previous
			s.mu.Unlock()
			return fail(err)
		}
	}
	if changed {
		if err := s.pm.UpdateBypassRules(s.cfg); err != nil {
			s.cfg.BypassCache = previous
			rollbackErr := saveConfig(s.cfgPath, s.cfg)
			s.mu.Unlock()
			return fail(fmt.Errorf("apply bypass: %w (restore cache: %v)", err, rollbackErr))
		}
	}
	s.bypassDiag.Transport = transport
	s.mu.Unlock()
	s.mu.Lock()
	s.bypassDiag.DurationMS = time.Since(started).Milliseconds()
	s.bypassDiag.SuccessAt = time.Now()
	status := s.bypassStatusLocked()
	s.mu.Unlock()
	s.pm.event(serviceLogInfo, "bypass", "refresh.succeeded", "белый список обновлён без перезапуска VPN",
		field("base_domains", status.Count))
	lease.Progress(operationProgress{Done: 1, Total: 1, Message: "Белый список обновлён"})
	return status, nil
}

func (s *apiServer) runBypassAutoUpdates() {
	time.Sleep(time.Duration(s.settingsSnapshot().SubscriptionFirstDelayMin) * time.Minute)
	for {
		_, err := s.refreshBypassLists(context.Background(), "background")
		interval := 24 * time.Hour
		if err != nil {
			interval = time.Hour
		}
		time.Sleep(interval)
	}
}
