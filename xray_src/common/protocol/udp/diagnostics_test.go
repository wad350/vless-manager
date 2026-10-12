package udp

import (
	"errors"
	"sync"
	"testing"

	"github.com/xtls/xray-core/common/buf"
)

func TestDiagnosticsOptInAndConcurrent(t *testing.T) {
	EnableDiagnostics(false)
	before := DiagnosticSnapshot()
	RecordReceived()
	RecordQueueDrop()
	RecordPipeDrop(2)
	RecordTruncated()
	RecordDispatched(2)
	RecordReply(nil)
	if got := DiagnosticSnapshot(); got["received"] != before["received"] || got["dispatched"] != before["dispatched"] {
		t.Fatal("disabled diagnostics changed counters")
	}
	EnableDiagnostics(true)
	t.Cleanup(func() { EnableDiagnostics(false) })
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() {
			for range 100 {
				RecordReceived()
				RecordQueueDrop()
				RecordPipeDrop(2)
				RecordTruncated()
				RecordDispatched(2)
				RecordReply(nil)
				RecordReply(errors.New("write failed"))
			}
		})
	}
	workers.Wait()
	after := DiagnosticSnapshot()
	for name, got := range after {
		if name == "vless_sent" || name == "vless_responses" || name == "vless_write_errors" {
			if got != before[name] {
				t.Fatalf("unused writer counter changed: %s", name)
			}
			continue
		}
		want := uint32(800)
		if name == "dispatched" || name == "pipe_drops" {
			want *= 2
		}
		if got-before[name] != want {
			t.Fatalf("%s delta = %d, want %d", name, got-before[name], want)
		}
	}
}

type testDiagnosticWriter struct {
	err                 error
	closed, interrupted bool
}

func (w *testDiagnosticWriter) WriteMultiBuffer(mb buf.MultiBuffer) error {
	buf.ReleaseMulti(mb)
	return w.err
}
func (w *testDiagnosticWriter) Close() error { w.closed = true; return nil }
func (w *testDiagnosticWriter) Interrupt()   { w.interrupted = true }

func TestVLESSDiagnosticsPreserveOwnershipAndClose(t *testing.T) {
	writer := &testDiagnosticWriter{}
	EnableDiagnostics(false)
	if TrackVLESSPackets(writer, true) != writer {
		t.Fatal("disabled diagnostics wrapped the writer")
	}
	EnableDiagnostics(true)
	t.Cleanup(func() { EnableDiagnostics(false) })
	before := DiagnosticSnapshot()
	for _, uplink := range []bool{true, false} {
		w := TrackVLESSPackets(writer, uplink).(*diagnosticWriter)
		packet := buf.New()
		packet.WriteString("packet")
		if err := w.WriteMultiBuffer(buf.MultiBuffer{packet}); err != nil || packet.Bytes() != nil {
			t.Fatal("writer lost ownership contract")
		}
		w.Close()
		w.Interrupt()
	}
	writer.err = errors.New("failed")
	w := TrackVLESSPackets(writer, true)
	packet := buf.New()
	packet.WriteString("failure")
	if w.WriteMultiBuffer(buf.MultiBuffer{packet}) != writer.err {
		t.Fatal("writer hid the error")
	}
	after := DiagnosticSnapshot()
	for _, key := range []string{"vless_sent", "vless_responses", "vless_write_errors"} {
		if after[key]-before[key] != 1 {
			t.Fatalf("%s delta = %d", key, after[key]-before[key])
		}
	}
	if !writer.closed || !writer.interrupted {
		t.Fatal("writer hid lifecycle interfaces")
	}
}
