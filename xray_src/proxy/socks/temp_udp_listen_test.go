package socks

import (
	"bytes"
	"errors"
	"net"
	"testing"
	"time"

	xnet "github.com/xtls/xray-core/common/net"
)

type auditPacketConn struct{ closes int }

func (*auditPacketConn) ReadFrom([]byte) (int, net.Addr, error)    { return 0, nil, net.ErrClosed }
func (*auditPacketConn) WriteTo(b []byte, _ net.Addr) (int, error) { return len(b), nil }
func (c *auditPacketConn) Close() error                            { c.closes++; return nil }
func (*auditPacketConn) LocalAddr() net.Addr                       { return &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)} }
func (*auditPacketConn) SetDeadline(time.Time) error               { return nil }
func (*auditPacketConn) SetReadDeadline(time.Time) error           { return nil }
func (*auditPacketConn) SetWriteDeadline(time.Time) error          { return nil }

type auditControlConn struct {
	writes    int
	closes    int
	failWrite int
}

var errAuditResponseFailure = errors.New("injected response failure")

func (*auditControlConn) Read([]byte) (int, error) { return 0, net.ErrClosed }
func (c *auditControlConn) Write(b []byte) (int, error) {
	c.writes++
	if c.writes == c.failWrite {
		return 0, errAuditResponseFailure
	}
	return len(b), nil
}
func (c *auditControlConn) Close() error                   { c.closes++; return nil }
func (*auditControlConn) LocalAddr() net.Addr              { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)} }
func (*auditControlConn) RemoteAddr() net.Addr             { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)} }
func (*auditControlConn) SetDeadline(time.Time) error      { return nil }
func (*auditControlConn) SetReadDeadline(time.Time) error  { return nil }
func (*auditControlConn) SetWriteDeadline(time.Time) error { return nil }

func TestTempUDPConnCloseBeforeSetTimeout(t *testing.T) {
	packet, control := &auditPacketConn{}, &auditControlConn{}
	c := NewTempUDPConn(packet, control, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if packet.closes != 1 || control.closes != 1 {
		t.Fatalf("cleanup calls: packet=%d control=%d", packet.closes, control.closes)
	}
}

func TestTempUDPConnCloseAfterSetTimeout(t *testing.T) {
	packet, control := &auditPacketConn{}, &auditControlConn{}
	c := NewTempUDPConn(packet, control, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	c.SetTimeout(time.Hour)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	// The timer callback also closes its connections; retain that lifecycle.
	if packet.closes == 0 || control.closes == 0 {
		t.Fatalf("connections not closed: packet=%d control=%d", packet.closes, control.closes)
	}
}

func TestUDPAssociateResponseFailureClosesBeforeSetTimeout(t *testing.T) {
	control := &auditControlConn{failWrite: 2}
	s := &ServerSession{
		config:       &ServerConfig{UdpEnabled: true},
		localAddress: xnet.LocalHostIP,
	}
	// Configure a loopback reply address so no non-local socket is created.
	// One NO_AUTH method, then UDP ASSOCIATE to 0.0.0.0:0.
	reader := bytes.NewReader([]byte{0, 5, 3, 0, 1, 0, 0, 0, 0, 0, 0})
	request, udpConn, err := s.handshake5(1, reader, control)
	if !errors.Is(err, errAuditResponseFailure) || request != nil || udpConn != nil || control.writes != 2 {
		t.Fatalf("expected response failure, got request=%v conn=%v err=%v", request, udpConn, err)
	}
	if control.closes == 0 {
		t.Fatal("associated TCP connection was not closed")
	}
}
