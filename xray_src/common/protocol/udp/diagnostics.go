package udp

import (
	"sync/atomic"

	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/buf"
)

var diagnosticsEnabled atomic.Bool

// Diagnostics are opt-in. Use 32-bit counters to avoid 64-bit atomic locks on MIPS.
var diagnostics struct {
	received, queueDrops, pipeDrops, truncated, dispatched, replies, writeErrors atomic.Uint32
	vlessSent, vlessResponses, vlessWriteErrors                                  atomic.Uint32
}

func EnableDiagnostics(enabled bool) { diagnosticsEnabled.Store(enabled) }

func RecordReceived() {
	if diagnosticsEnabled.Load() {
		diagnostics.received.Add(1)
	}
}

func RecordQueueDrop() {
	if diagnosticsEnabled.Load() {
		diagnostics.queueDrops.Add(1)
	}
}

func RecordPipeDrop(packets int) {
	if diagnosticsEnabled.Load() {
		diagnostics.pipeDrops.Add(uint32(packets))
	}
}

func RecordTruncated() {
	if diagnosticsEnabled.Load() {
		diagnostics.truncated.Add(1)
	}
}

func RecordDispatched(packets int) {
	if diagnosticsEnabled.Load() {
		diagnostics.dispatched.Add(uint32(packets))
	}
}

func RecordReply(err error) {
	if !diagnosticsEnabled.Load() {
		return
	}
	if err == nil {
		diagnostics.replies.Add(1)
	} else {
		diagnostics.writeErrors.Add(1)
	}
}

type diagnosticWriter struct {
	buf.Writer
	uplink bool
}

// TrackVLESSPackets counts payload accepted by the body writer, not delivery
// at the remote peer. Disabled diagnostics preserve the original writer.
func TrackVLESSPackets(writer buf.Writer, uplink bool) buf.Writer {
	if !diagnosticsEnabled.Load() {
		return writer
	}
	return &diagnosticWriter{Writer: writer, uplink: uplink}
}

func (w *diagnosticWriter) WriteMultiBuffer(mb buf.MultiBuffer) error {
	packets := len(mb)
	err := w.Writer.WriteMultiBuffer(mb)
	if err != nil {
		diagnostics.vlessWriteErrors.Add(1)
	} else if w.uplink {
		diagnostics.vlessSent.Add(uint32(packets))
	} else {
		diagnostics.vlessResponses.Add(uint32(packets))
	}
	return err
}

func (w *diagnosticWriter) Close() error { return common.Close(w.Writer) }
func (w *diagnosticWriter) Interrupt()   { common.Interrupt(w.Writer) }

func DiagnosticSnapshot() map[string]uint32 {
	return map[string]uint32{
		"received": diagnostics.received.Load(), "queue_drops": diagnostics.queueDrops.Load(),
		"pipe_drops": diagnostics.pipeDrops.Load(),
		"vless_sent": diagnostics.vlessSent.Load(), "vless_responses": diagnostics.vlessResponses.Load(),
		"vless_write_errors": diagnostics.vlessWriteErrors.Load(),
		"truncated":          diagnostics.truncated.Load(), "dispatched": diagnostics.dispatched.Load(),
		"replies": diagnostics.replies.Load(), "write_errors": diagnostics.writeErrors.Load(),
	}
}
