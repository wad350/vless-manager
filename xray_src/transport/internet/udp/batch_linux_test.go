//go:build linux

package udp

import (
	"bytes"
	"encoding/binary"
	"net"
	"testing"
	"time"

	"github.com/xtls/xray-core/common/buf"
	packet "github.com/xtls/xray-core/common/protocol/udp"
	"golang.org/x/sys/unix"
)

func TestBatchReceiveKeepsPacketsAndOriginalDestination(t *testing.T) {
	for _, mode := range []string{"on", "off"} {
		for _, network := range []string{"udp4", "udp"} {
			t.Run(mode+"/"+network, func(t *testing.T) {
				t.Setenv("XRAY_TPROXY_UDP_BATCH", mode)
				testBatchReceive(t, network)
			})
		}
	}
}

func testBatchReceive(t *testing.T, network string) {
	listener, err := net.ListenUDP(network, &net.UDPAddr{IP: net.IPv4zero})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	raw, err := listener.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var sockErr error
	if err := raw.Control(func(fd uintptr) {
		sockErr = unix.SetsockoptInt(int(fd), unix.SOL_IP, unix.IP_RECVORIGDSTADDR, 1)
	}); err != nil || sockErr != nil {
		t.Fatalf("original destination option: %v %v", err, sockErr)
	}
	hub := &Hub{conn: listener, udpConn: listener, recvOrigDest: true, cache: make(chan *packet.Packet, 64)}
	finished := make(chan struct{})
	go func() { hub.start(); close(finished) }()
	defer func() { listener.Close(); <-finished }()
	var senders [4]*net.UDPConn
	for i := range senders {
		senders[i], err = net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			t.Fatal(err)
		}
		defer senders[i].Close()
	}
	port := listener.LocalAddr().(*net.UDPAddr).Port
	var payloads [48][]byte
	for i := range 48 {
		payload := make([]byte, 64+(i%7)*193)
		for j := range payload {
			payload[j] = byte(i*17 + j*5)
		}
		binary.BigEndian.PutUint32(payload, uint32(i))
		payloads[i] = payload
		dest := &net.UDPAddr{IP: net.IPv4(127, 0, 0, byte(1+i%2)), Port: port}
		if _, err := senders[i%len(senders)].WriteToUDP(payload, dest); err != nil {
			t.Fatal(err)
		}
	}
	seen := make(map[uint32]bool)
	deadline := time.After(3 * time.Second)
	for range 48 {
		select {
		case p := <-hub.Receive():
			if p == nil {
				t.Fatal("hub closed early")
			}
			i := binary.BigEndian.Uint32(p.Payload.Bytes())
			if seen[i] || i >= 48 || !bytes.Equal(p.Payload.Bytes(), payloads[i]) {
				t.Fatalf("corrupted/duplicate packet: %d", i)
			}
			seen[i] = true
			if !p.Target.IsValid() || p.Target.Address.String() != net.IPv4(127, 0, 0, byte(1+i%2)).String() || int(p.Target.Port) != port {
				t.Fatalf("original destination changed for %d: %v", i, p.Target)
			}
			if int(p.Source.Port) != senders[int(i)%len(senders)].LocalAddr().(*net.UDPAddr).Port {
				t.Fatalf("source port changed: %v", p.Source)
			}
			p.Payload.Release()
		case <-deadline:
			t.Fatal("batch read stalled or packets lost")
		}
	}
	// One packet must be delivered immediately, without filling all eight slots.
	if _, err := senders[0].WriteToUDP([]byte("single"), &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port}); err != nil {
		t.Fatal(err)
	}
	select {
	case p := <-hub.Receive():
		if string(p.Payload.Bytes()) != "single" {
			t.Fatal("single packet corrupted")
		}
		p.Payload.Release()
	case <-time.After(time.Second):
		t.Fatal("single packet waiting for full batch")
	}
	// A truncated datagram must never become a valid packet, nor expose a
	// recycled buffer's unused tail in the following short datagram.
	for _, payload := range [][]byte{bytes.Repeat([]byte{0xa5}, buf.Size+1), []byte("after-oversized")} {
		if _, err := senders[0].WriteToUDP(payload, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port}); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case p := <-hub.Receive():
		if string(p.Payload.Bytes()) != "after-oversized" {
			t.Fatal("truncated datagram or unused tail forwarded")
		}
		p.Payload.Release()
	case <-time.After(time.Second):
		t.Fatal("short packet lost after truncation")
	}
}

func TestBatchReceiveOptOut(t *testing.T) {
	t.Setenv("XRAY_TPROXY_UDP_BATCH", "off")
	listener, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if (&Hub{udpConn: listener, recvOrigDest: true}).startBatch() {
		t.Fatal("batch enabled despite explicit opt-out")
	}
}

func TestEnqueueOverflowReleasesPacket(t *testing.T) {
	hub := &Hub{cache: make(chan *packet.Packet)}
	b := buf.New()
	b.WriteString("packet")
	hub.enqueue(b, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1234}, nil)
	if b.Len() != 0 {
		t.Fatal("overflow packet retained")
	}
}
