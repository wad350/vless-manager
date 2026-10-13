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
	data := pkt.Data().AsRange().ToSlice()
	pkt.DecRef()
	if !bytes.Equal(data, want) {
		t.Fatal("queued UDP payload did not survive releasing its source packet")
	}
	data[0] = 99
	if want[0] != 1 {
		t.Fatal("queued UDP payload aliases the source buffer")
	}
}
