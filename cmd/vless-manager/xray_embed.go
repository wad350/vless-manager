package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "github.com/xtls/xray-core/app/dispatcher"
	_ "github.com/xtls/xray-core/app/observatory"
	_ "github.com/xtls/xray-core/app/proxyman/inbound"
	_ "github.com/xtls/xray-core/app/proxyman/outbound"
	_ "github.com/xtls/xray-core/app/router"
	_ "github.com/xtls/xray-core/app/stats"
	xlog "github.com/xtls/xray-core/common/log"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/features/routing"
	"github.com/xtls/xray-core/features/stats"
	"github.com/xtls/xray-core/infra/conf"
	_ "github.com/xtls/xray-core/proxy/dokodemo"
	_ "github.com/xtls/xray-core/proxy/freedom"
	_ "github.com/xtls/xray-core/proxy/socks"
	_ "github.com/xtls/xray-core/proxy/vless/outbound"
	_ "github.com/xtls/xray-core/transport/internet/grpc"
	_ "github.com/xtls/xray-core/transport/internet/httpupgrade"
	_ "github.com/xtls/xray-core/transport/internet/reality"
	_ "github.com/xtls/xray-core/transport/internet/splithttp"
	_ "github.com/xtls/xray-core/transport/internet/tagged/taggedimpl"
	_ "github.com/xtls/xray-core/transport/internet/tcp"
	_ "github.com/xtls/xray-core/transport/internet/tls"
	_ "github.com/xtls/xray-core/transport/internet/websocket"
)

type engineHandle interface {
	Close() error
	TrafficSnapshot() outboundTrafficSnapshot
}

type xrayLogTarget struct {
	rb       *ringBuffer
	level    serviceLogLevel
	disabled bool
}
type xrayLogBridge struct{ target atomic.Pointer[xrayLogTarget] }

var engineLogs xrayLogBridge
var engineLogOnce sync.Once

func (b *xrayLogBridge) Handle(message xlog.Message) {
	target := b.target.Load()
	if target == nil || target.disabled {
		return
	}
	general, ok := message.(*xlog.GeneralMessage)
	if !ok {
		return
	}
	level := serviceLogError
	switch general.Severity {
	case xlog.Severity_Warning:
		level = serviceLogWarn
	case xlog.Severity_Info:
		level = serviceLogInfo
	case xlog.Severity_Debug:
		level = serviceLogDebug
	}
	if level > target.level {
		return
	}
	target.rb.logEventUnfiltered(level, "xray", "runtime", fmt.Sprint(general.Content), field("xray_level", strings.ToLower(serviceLogLevelName(level))))
}

type embeddedXray struct {
	*core.Instance
	stats         stats.Manager
	proxyTags     []string
	logTarget     *xrayLogTarget
	cancel        context.CancelFunc
	ctx           context.Context
	routingConfig *conf.RouterConfig
	proxyRule     map[string]any
}

const xrayTunnelTestTag = "manager-tunnel-test"

type xrayClientConn struct {
	net.Conn
	cancel context.CancelFunc
}

func (c *xrayClientConn) Close() error {
	c.cancel()
	return c.Conn.Close()
}

func (b *embeddedXray) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if network != "tcp" && network != "tcp4" && network != "tcp6" {
		return nil, fmt.Errorf("tunnel HTTP dial requires TCP, got %s", network)
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	p, err := xnet.PortFromString(port)
	if err != nil {
		return nil, err
	}
	// HTTP connection reuse outlives the transport's dial context. Each pipe
	// belongs to this core and is cancelled on connection close or core stop.
	streamCtx, cancel := context.WithCancel(b.ctx)
	streamCtx = session.ContextWithInbound(streamCtx, &session.Inbound{Tag: xrayTunnelTestTag})
	conn, err := core.Dial(streamCtx, b.Instance, xnet.TCPDestination(xnet.ParseAddress(host), p))
	if err != nil {
		cancel()
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		cancel()
		conn.Close()
		return nil, err
	}
	return &xrayClientConn{Conn: conn, cancel: cancel}, nil
}

func (b *embeddedXray) Close() error {
	if b.cancel != nil {
		b.cancel()
	}
	err := b.Instance.Close()
	if b.logTarget != nil {
		engineLogs.target.CompareAndSwap(b.logTarget, nil)
	}
	return err
}

func (b *embeddedXray) UpdateBypassRules(cfg *Config) error {
	if b.routingConfig == nil || b.proxyRule == nil {
		return fmt.Errorf("Xray routing configuration unavailable")
	}
	rules := xrayRoutingRules(cfg, b.proxyRule)
	updated := *b.routingConfig
	updated.RuleList = make([]json.RawMessage, 0, len(rules))
	for _, rule := range rules {
		data, err := json.Marshal(rule)
		if err != nil {
			return err
		}
		updated.RuleList = append(updated.RuleList, data)
	}
	built, err := updated.Build()
	if err != nil {
		return err
	}
	router, ok := b.GetFeature(routing.RouterType()).(routing.Router)
	if !ok {
		return fmt.Errorf("Xray router unavailable")
	}
	return router.AddRule(serial.ToTypedMessage(built), false)
}

func (b *embeddedXray) TrafficSnapshot() outboundTrafficSnapshot {
	read := func(tag, direction string) uint64 {
		if b.stats == nil {
			return 0
		}
		if counter := b.stats.GetCounter("outbound>>>" + tag + ">>>traffic>>>" + direction); counter != nil {
			return uint64(max(0, counter.Value()))
		}
		return 0
	}
	result := outboundTrafficSnapshot{BypassUpload: read("direct", "uplink"), BypassDownload: read("direct", "downlink")}
	for _, tag := range b.proxyTags {
		result.VPNUpload += read(tag, "uplink")
		result.VPNDownload += read(tag, "downlink")
	}
	return result
}

func parseXrayConfig(data []byte) (*core.Config, *conf.Config, error) {
	engineLogOnce.Do(func() { xlog.RegisterHandler(&engineLogs) })
	var cfg conf.Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, nil, err
	}
	built, err := cfg.Build()
	if err != nil {
		return nil, nil, err
	}
	// Xray's logger is global. Temporary ping instances must not replace or
	// close the active tunnel's logger; one bridge owns logging for the process.
	apps := built.App[:0]
	for _, app := range built.App {
		if app.Type != "xray.app.log.Config" {
			apps = append(apps, app)
		}
	}
	built.App = apps
	return built, &cfg, nil
}

func startEmbedded(data []byte, logs *ringBuffer) (engineHandle, error) {
	return startXray(data, logs, true)
}

func startXray(data []byte, logs *ringBuffer, mainTunnel bool) (engineHandle, error) {
	built, cfg, err := parseXrayConfig(data)
	if err != nil {
		return nil, fmt.Errorf("parse Xray config: %w", err)
	}
	var target *xrayLogTarget
	if mainTunnel {
		var raw struct {
			Log struct {
				Level string `json:"loglevel"`
			} `json:"log"`
		}
		_ = json.Unmarshal(data, &raw)
		level := raw.Log.Level
		if level == "warning" {
			level = "warn"
		}
		target = &xrayLogTarget{rb: logs, level: parseServiceLogLevel(level), disabled: level == "none"}
		engineLogs.target.Store(target)
	}
	ctx, cancel := context.WithCancel(context.Background())
	instance, err := core.NewWithContext(ctx, built)
	if err != nil {
		cancel()
		if target != nil {
			engineLogs.target.CompareAndSwap(target, nil)
		}
		return nil, fmt.Errorf("create Xray: %w", err)
	}
	b := &embeddedXray{Instance: instance, logTarget: target, cancel: cancel, ctx: ctx}
	b.routingConfig = cfg.RouterConfig
	if cfg.RouterConfig != nil && len(cfg.RouterConfig.RuleList) > 0 {
		if err := json.Unmarshal(cfg.RouterConfig.RuleList[len(cfg.RouterConfig.RuleList)-1], &b.proxyRule); err != nil {
			_ = b.Close()
			return nil, fmt.Errorf("read Xray proxy rule: %w", err)
		}
	}
	b.stats, _ = instance.GetFeature(stats.ManagerType()).(stats.Manager)
	for _, out := range cfg.OutboundConfigs {
		if out.Tag == "proxy" || strings.HasPrefix(out.Tag, "proxy-") {
			b.proxyTags = append(b.proxyTags, out.Tag)
		}
	}
	if err := instance.Start(); err != nil {
		_ = b.Close()
		return nil, fmt.Errorf("start Xray: %w", err)
	}
	return b, nil
}

func startTemporaryVLESSSOCKS(srv *VLESSServer, probeURL string, logs *ringBuffer) (engineHandle, int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, 0, err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	cfg, err := xrayBaseConfig(srv, probeURL)
	if err != nil {
		return nil, 0, err
	}
	cfg["inbounds"] = []any{xraySOCKSInbound(port)}
	data, err := json.Marshal(cfg)
	if err != nil {
		return nil, 0, err
	}
	instance, err := startXray(data, logs, false)
	return instance, port, err
}

var pingStartupWait = 300 * time.Millisecond

func pingOneThroughVLESSContext(ctx context.Context, srv *VLESSServer, timeout time.Duration, testURL string) PingResult {
	failed := PingResult{ServerID: srv.ID, ServerName: srv.Name, Address: srv.Address, Port: srv.Port, Protocol: describeProtocol(srv), LatencyMS: -1, CheckedAt: time.Now()}
	if err := ctx.Err(); err != nil {
		failed.Error = err.Error()
		return failed
	}
	b, port, err := startTemporaryVLESSSOCKS(srv, testURL, newRingBuffer())
	if err != nil {
		failed.Error = err.Error()
		return failed
	}
	defer b.Close()
	if d := pingStartupWait; d > 0 {
		timer := time.NewTimer(d)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			failed.Error = ctx.Err().Error()
			return failed
		}
	}
	result := pingViaSOCKSContext(ctx, srv, port, timeout, testURL)
	result.Protocol, result.CheckedAt = failed.Protocol, time.Now()
	return result
}
