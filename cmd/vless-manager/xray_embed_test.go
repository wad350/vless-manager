package main

import (
	xlog "github.com/xtls/xray-core/common/log"
	"testing"
)

func TestXrayLogBridgePreservesLevelAndFiltersIndependently(t *testing.T) {
	rb := newRingBuffer()
	rb.setLevel("error")
	bridge := &xrayLogBridge{}
	bridge.target.Store(&xrayLogTarget{rb: rb, level: serviceLogWarn})
	bridge.Handle(&xlog.GeneralMessage{Severity: xlog.Severity_Warning, Content: "warning"})
	bridge.Handle(&xlog.GeneralMessage{Severity: xlog.Severity_Debug, Content: "details"})
	entries, _ := rb.Entries(0)
	if len(entries) != 1 || entries[0].Level != "WARN" || entries[0].Fields["xray_level"] != "warn" {
		t.Fatalf("unexpected filtered logs: %+v", entries)
	}
	bridge.target.Store(&xrayLogTarget{rb: rb, disabled: true})
	bridge.Handle(&xlog.GeneralMessage{Severity: xlog.Severity_Error, Content: "disabled"})
	entries, _ = rb.Entries(0)
	if len(entries) != 1 {
		t.Fatal("disabled logging emitted an entry")
	}
}
