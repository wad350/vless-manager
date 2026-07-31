//go:build with_utls

// Implements boxHandle using embedded sing-box library.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"sync/atomic"
	"time"

	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/endpoint"
	"github.com/sagernet/sing-box/adapter/inbound"
	"github.com/sagernet/sing-box/adapter/outbound"
	boxService "github.com/sagernet/sing-box/adapter/service"
	"github.com/sagernet/sing-box/dns"
	"github.com/sagernet/sing-box/dns/transport/local"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/protocol/direct"
	"github.com/sagernet/sing-box/protocol/group"
	"github.com/sagernet/sing-box/protocol/socks"
	tuninbound "github.com/sagernet/sing-box/protocol/tun"
	"github.com/sagernet/sing-box/protocol/vless"
	singBufio "github.com/sagernet/sing/common/bufio"
	singJSON "github.com/sagernet/sing/common/json"
	N "github.com/sagernet/sing/common/network"
)

// boxLogWriter bridges sing-box log output into our ring buffer.
type boxLogWriter struct{ rb *ringBuffer }

func (w *boxLogWriter) DisableColors() bool { return true }
func (w *boxLogWriter) WriteMessage(level log.Level, message string) {
	serviceLevel := serviceLogInfo
	switch level {
	case log.LevelPanic, log.LevelFatal, log.LevelError:
		serviceLevel = serviceLogError
	case log.LevelWarn:
		serviceLevel = serviceLogWarn
	case log.LevelDebug:
		serviceLevel = serviceLogDebug
	case log.LevelTrace:
		serviceLevel = serviceLogTrace
	}
	w.rb.logEventUnfiltered(serviceLevel, "sing-box", "runtime", message,
		field("singbox_level", log.FormatLevel(level)))
}

// boxHandle is the interface used by ProcessManager to control sing-box.
// Defined in this file so the with_utls build satisfies it; the stub file
// satisfies it for builds without the tag.
type boxHandle interface {
	Close() error
	TrafficSnapshot() outboundTrafficSnapshot
}

type embeddedBox struct {
	*box.Box
	traffic *outboundTrafficTracker
}

func (b *embeddedBox) TrafficSnapshot() outboundTrafficSnapshot {
	return b.traffic.Snapshot()
}

// startEmbedded builds and starts an embedded sing-box instance.
func startEmbedded(cfgJSON []byte, logs *ringBuffer) (boxHandle, error) {
	ctx := context.Background()
	b, err := newBoxInstance(ctx, cfgJSON, logs)
	if err != nil {
		return nil, fmt.Errorf("create sing-box: %w", err)
	}
	traffic := &outboundTrafficTracker{}
	b.Router().AppendTracker(traffic)
	if err := b.Start(); err != nil {
		_ = b.Close()
		return nil, fmt.Errorf("start sing-box: %w", err)
	}
	return &embeddedBox{Box: b, traffic: traffic}, nil
}

type outboundTrafficTracker struct {
	vpnUpload      atomic.Int64
	vpnDownload    atomic.Int64
	bypassUpload   atomic.Int64
	bypassDownload atomic.Int64
}

func (t *outboundTrafficTracker) RoutedConnection(
	_ context.Context,
	conn net.Conn,
	_ adapter.InboundContext,
	_ adapter.Rule,
	matchOutbound adapter.Outbound,
) net.Conn {
	upload, download := t.counters(matchOutbound)
	if upload == nil {
		return conn
	}
	return singBufio.NewInt64CounterConn(
		conn,
		[]*atomic.Int64{upload},
		[]*atomic.Int64{download},
	)
}

func (t *outboundTrafficTracker) RoutedPacketConnection(
	_ context.Context,
	conn N.PacketConn,
	_ adapter.InboundContext,
	_ adapter.Rule,
	matchOutbound adapter.Outbound,
) N.PacketConn {
	upload, download := t.counters(matchOutbound)
	if upload == nil {
		return conn
	}
	return singBufio.NewInt64CounterPacketConn(
		conn,
		[]*atomic.Int64{upload},
		nil,
		[]*atomic.Int64{download},
		nil,
	)
}

func (t *outboundTrafficTracker) counters(outbound adapter.Outbound) (upload, download *atomic.Int64) {
	if outbound == nil {
		return nil, nil
	}
	return t.countersForTag(outbound.Tag())
}

func (t *outboundTrafficTracker) countersForTag(tag string) (upload, download *atomic.Int64) {
	switch tag {
	case "proxy":
		return &t.vpnUpload, &t.vpnDownload
	case "direct":
		return &t.bypassUpload, &t.bypassDownload
	default:
		return nil, nil
	}
}

func (t *outboundTrafficTracker) Snapshot() outboundTrafficSnapshot {
	return outboundTrafficSnapshot{
		VPNDownload:    uint64(max(0, t.vpnDownload.Load())),
		VPNUpload:      uint64(max(0, t.vpnUpload.Load())),
		BypassDownload: uint64(max(0, t.bypassDownload.Load())),
		BypassUpload:   uint64(max(0, t.bypassUpload.Load())),
	}
}

// pingOneThroughVLESS measures real VLESS latency (like happ/v2rayN) by
// starting a temporary embedded sing-box with only a SOCKS5 inbound and this
// server as the outbound, then doing HTTP GET to testURL through it.
//
// `testURL` is the AppSettings.PingTestURL (with defaultPingTestURL fallback);
// the caller passes it through so a single setting drives every probe in
// pingBatchViaSingBox.
func pingOneThroughVLESS(srv *VLESSServer, timeout time.Duration, testURL string) PingResult {
	return pingOneThroughVLESSContext(context.Background(), srv, timeout, testURL)
}

func pingOneThroughVLESSContext(ctx context.Context, srv *VLESSServer, timeout time.Duration, testURL string) PingResult {
	b, port, err := startTemporaryVLESSSOCKS(srv, newRingBuffer())
	if err != nil {
		r := pingTCPFallback(srv, timeout)
		r.Error = err.Error()
		return r
	}
	defer b.Close()

	if d := pingStartupWait; d > 0 {
		timer := time.NewTimer(d)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			r := pingTCPFallback(srv, time.Millisecond)
			r.Error = ctx.Err().Error()
			return r
		}
	}

	return pingViaSOCKSContext(ctx, srv, port, timeout, testURL)
}

// startTemporaryVLESSSOCKS starts a small SOCKS-only sing-box instance whose
// sole outbound is srv. The VLESS socket carries WANFwmark, so it cannot loop
// into the main TUN when the global tunnel is already running.
func startTemporaryVLESSSOCKS(srv *VLESSServer, logs *ringBuffer) (boxHandle, int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, 0, err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	proxyOutbounds, err := buildSingBoxProxyOutbounds(srv)
	if err != nil {
		return nil, 0, err
	}
	outbounds := make([]any, 0, len(proxyOutbounds)+1)
	for _, outbound := range proxyOutbounds {
		outbounds = append(outbounds, outbound)
	}
	outbounds = append(outbounds, map[string]any{"type": "direct", "tag": "direct"})
	cfg := map[string]any{
		"log": map[string]any{
			"level": "error", "timestamp": false, "output": os.DevNull,
		},
		"inbounds": []any{map[string]any{
			"type": "socks", "tag": "socks-ping",
			"listen": "127.0.0.1", "listen_port": port,
		}},
		"outbounds": outbounds,
		"route":     map[string]any{"final": "proxy"},
	}
	cfgJSON, err := json.Marshal(cfg)
	if err != nil {
		return nil, 0, err
	}

	b, err := startEmbedded(cfgJSON, logs)
	if err != nil {
		return nil, 0, fmt.Errorf("box start: %w", err)
	}
	return b, port, nil
}

// pingStartupWait is set by runPingAll right before each batch so the
// embedded helper can pick it up without touching its function signature.
// 0 keeps the historical 300 ms behaviour via runtime fallback.
var pingStartupWait = 300 * time.Millisecond

// newBoxInstance creates a sing-box Box from a JSON config.
// Only the protocols we actually use are registered (no TUN, no gVisor).
func newBoxInstance(ctx context.Context, cfgJSON []byte, logs *ringBuffer) (*box.Box, error) {
	inboundReg := inbound.NewRegistry()
	tuninbound.RegisterInbound(inboundReg)
	socks.RegisterInbound(inboundReg)

	outboundReg := outbound.NewRegistry()
	direct.RegisterOutbound(outboundReg)
	group.RegisterURLTest(outboundReg)
	vless.RegisterOutbound(outboundReg)

	endpointReg := endpoint.NewRegistry()

	dnsReg := dns.NewTransportRegistry()
	local.RegisterTransport(dnsReg)

	serviceReg := boxService.NewRegistry()

	ctx = box.Context(ctx, inboundReg, outboundReg, endpointReg, dnsReg, serviceReg)

	var opts option.Options
	if err := singJSON.UnmarshalContext(ctx, cfgJSON, &opts); err != nil {
		return nil, fmt.Errorf("parse sing-box config: %w", err)
	}

	instance, err := box.New(box.Options{
		Context:           ctx,
		Options:           opts,
		PlatformLogWriter: &boxLogWriter{rb: logs},
	})
	if err != nil {
		return nil, err
	}
	return instance, nil
}
