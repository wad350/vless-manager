//go:build !with_utls

package main

import (
	"fmt"
	"time"
)

// boxHandle stub — satisfies the interface when not building with sing-box.
type boxHandle interface {
	Close() error
	TrafficSnapshot() outboundTrafficSnapshot
}

type stubBox struct{}

func (s *stubBox) Close() error { return nil }
func (s *stubBox) TrafficSnapshot() outboundTrafficSnapshot {
	return outboundTrafficSnapshot{}
}

func startEmbedded(_ []byte, _ *ringBuffer) (boxHandle, error) {
	return nil, fmt.Errorf("built without -tags with_utls: sing-box not embedded")
}

func pingOneThroughVLESS(srv *VLESSServer, timeout time.Duration, _ string) PingResult {
	return pingTCPFallback(srv, timeout)
}

func startTemporaryVLESSSOCKS(_ *VLESSServer, _ *ringBuffer) (boxHandle, int, error) {
	return nil, 0, fmt.Errorf("embedded sing-box unavailable: build with -tags with_utls")
}

// pingStartupWait is referenced by ping.go for parity with the embedded build.
// In the stub build there is no sing-box subprocess to warm up, so this stays
// at zero.
var pingStartupWait time.Duration
