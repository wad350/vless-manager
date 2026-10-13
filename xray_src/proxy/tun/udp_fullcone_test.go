package tun

import (
	"bytes"
	"io"
	"testing"

	"github.com/xtls/xray-core/common/buf"
	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/xudp"
	"github.com/xtls/xray-core/transport/internet/stat"
)

func TestUDPQueuePreservesPayloadAndFullConeDestinations(t *testing.T) {
	src := net.UDPDestination(net.LocalHostIP, 12345)
	dest := net.UDPDestination(net.LocalHostIP, 53)
	other := net.UDPDestination(net.LocalHostIP, 443)
	connReady := make(chan *udpConn, 1)
	handler := newUdpConnectionHandler(func(conn net.Conn, _ net.Destination) {
		connReady <- conn.(*udpConn)
	}, nil)
	first := bytes.Repeat([]byte{1, 2, 3}, 400)
	second := bytes.Repeat([]byte{4, 5, 6}, 400)
	handler.HandlePacket(src, dest, first)
	conn := <-connReady
	handler.HandlePacket(src, other, second)
	var encoded buf.MultiBufferContainer
	// PacketReader decodes server replies on an established XUDP session.
	streamDest := dest
	streamDest.Network = net.Network_Unknown
	writer := xudp.NewPacketWriter(&encoded, streamDest, [8]byte{})
	// The real manager enables inbound stats; that wrapper must not erase UDP
	// destinations or force a second payload copy through net.Conn.Read.
	readerWithStats := buf.NewReader(&stat.CounterConnection{Connection: conn})
	for i, want := range [][]byte{first, second} {
		mb, err := readerWithStats.ReadMultiBuffer()
		if err != nil || len(mb) != 1 || !bytes.Equal(mb[0].Bytes(), want) {
			t.Fatalf("UDP packet %d: buffers=%v error=%v", i, mb, err)
		}
		wantDest := dest
		if i == 1 {
			wantDest = other
		}
		if *mb[0].UDP != wantDest {
			t.Fatal("full-cone destination lost")
		}
		if err := writer.WriteMultiBuffer(mb); err != nil {
			t.Fatal(err)
		}
	}
	reader := xudp.NewPacketReader(&encoded)
	for _, want := range [][]byte{first, second} {
		mb, err := reader.ReadMultiBuffer()
		if err != nil || len(mb) != 1 || !bytes.Equal(mb[0].Bytes(), want) {
			t.Fatalf("XUDP round trip failed: %v", err)
		}
		buf.ReleaseMulti(mb)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ReadMultiBuffer(); err != io.EOF {
		t.Fatalf("closed UDP queue: %v", err)
	}
}
