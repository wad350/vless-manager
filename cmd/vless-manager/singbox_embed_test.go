//go:build with_utls

package main

import (
	"testing"

	"github.com/sagernet/sing-box/log"
)

func TestSingBoxLogWriterPreservesLevel(t *testing.T) {
	rb := newRingBuffer()
	rb.setLevel("trace")
	writer := &boxLogWriter{rb: rb}
	writer.WriteMessage(log.LevelWarn, "warning")
	writer.WriteMessage(log.LevelDebug, "details")

	entries, _ := rb.Entries(0)
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
	if entries[0].Level != "WARN" || entries[0].Fields["singbox_level"] != "warn" {
		t.Fatalf("warning level was not preserved: %+v", entries[0])
	}
	if entries[1].Level != "DEBUG" || entries[1].Fields["singbox_level"] != "debug" {
		t.Fatalf("debug level was not preserved: %+v", entries[1])
	}
}

func TestOutboundTrafficTrackerSeparatesVPNAndBypass(t *testing.T) {
	tracker := &outboundTrafficTracker{}
	vpnUp, vpnDown := tracker.countersForTag("proxy")
	directUp, directDown := tracker.countersForTag("direct")
	vpnUp.Add(120)
	vpnDown.Add(900)
	directUp.Add(30)
	directDown.Add(70)

	got := tracker.Snapshot()
	if got.VPNUpload != 120 || got.VPNDownload != 900 {
		t.Fatalf("unexpected VPN counters: %+v", got)
	}
	if got.BypassUpload != 30 || got.BypassDownload != 70 {
		t.Fatalf("unexpected bypass counters: %+v", got)
	}
	if up, down := tracker.countersForTag("unknown"); up != nil || down != nil {
		t.Fatal("unknown outbound must not be counted")
	}
}
