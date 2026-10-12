package tun

import (
	"bytes"
	"testing"

	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
)

func TestUDPPayloadOwnsItsData(t *testing.T) {
	want := bytes.Repeat([]byte{1, 2, 3, 4}, 512)
	pkt := stack.NewPacketBuffer(stack.PacketBufferOptions{Payload: buffer.MakeWithData(want)})
	data := copyUDPPayload(pkt)
	pkt.DecRef()
	if !bytes.Equal(data, want) {
		t.Fatal("queued UDP payload did not survive releasing its source packet")
	}
	data[0] = 99
	if want[0] != 1 {
		t.Fatal("queued UDP payload aliases the source buffer")
	}
}

func BenchmarkUDPPayload(b *testing.B) {
	for _, cloneFirst := range []bool{false, true} {
		name := "direct"
		if cloneFirst {
			name = "legacy_clone"
		}
		b.Run(name, func(b *testing.B) {
			pkt := stack.NewPacketBuffer(stack.PacketBufferOptions{Payload: buffer.MakeWithData(make([]byte, 1200))})
			defer pkt.DecRef()
			b.ReportAllocs()
			b.SetBytes(1200)
			for i := 0; i < b.N; i++ {
				if cloneFirst {
					cloned := pkt.Clone()
					_ = cloned.Data().AsRange().ToSlice()
					cloned.DecRef()
				} else {
					_ = copyUDPPayload(pkt)
				}
			}
		})
	}
}
