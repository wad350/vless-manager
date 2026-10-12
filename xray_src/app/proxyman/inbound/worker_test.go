package inbound

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/xtls/xray-core/common/buf"
	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/signal/done"
	"github.com/xtls/xray-core/transport/internet"
	"github.com/xtls/xray-core/transport/pipe"
)

func TestUDPConnCloseDiscardsBufferedPackets(t *testing.T) {
	reader, writer := pipe.New(pipe.DiscardOverflow(), pipe.WithSizeLimit(16*1024))
	packet := buf.New()
	packet.WriteString("queued packet")
	if err := writer.WriteMultiBuffer(buf.MultiBuffer{packet}); err != nil {
		t.Fatal(err)
	}
	conn := &udpConn{reader: reader, writer: writer, done: done.New()}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if packet.Bytes() != nil {
		t.Fatal("closed UDP connection retained a queued packet")
	}
	if mb, err := reader.ReadMultiBuffer(); err == nil || mb != nil {
		buf.ReleaseMulti(mb)
		t.Fatalf("closed read = %v, %v", mb, err)
	}
}

func TestUDPWorkerCloseCancelsSessions(t *testing.T) {
	worker, id := testUDPWorker()
	conn, _ := worker.getConnection(id)
	if err := worker.Close(); err != nil {
		t.Fatal(err)
	}
	if conn.ctx.Err() != context.Canceled || !conn.done.Done() || len(worker.activeConn) != 0 {
		t.Fatalf("sessions retained: canceled=%v done=%v sessions=%d", conn.ctx.Err(), conn.done.Done(), len(worker.activeConn))
	}
	packet := buf.New()
	packet.WriteString("after shutdown")
	worker.callback(packet, id.src, id.dest)
	if packet.Bytes() != nil || len(worker.activeConn) != 0 {
		t.Fatal("closed worker accepted another UDP packet")
	}
}

func testUDPWorker() (*udpWorker, connID) {
	id := connID{
		src:  net.UDPDestination(net.LocalHostIP, 42001),
		dest: net.UDPDestination(net.IPAddress([]byte{1, 1, 1, 1}), 53),
	}
	return &udpWorker{ctx: context.Background(), address: net.LocalHostIP, port: 7892, activeConn: make(map[connID]*udpConn)}, id
}

func TestUDPOldSessionCannotRemoveReplacement(t *testing.T) {
	worker, id := testUDPWorker()
	t.Cleanup(func() { worker.Close() })
	old, _ := worker.getConnection(id)
	old.Close()
	replacement, existing := worker.getConnection(id)
	if existing || replacement == old {
		t.Fatal("closed session was reused")
	}
	worker.removeConn(id, old)
	if got, existing := worker.getConnection(id); !existing || got != replacement {
		t.Fatal("old session completion removed the replacement")
	}
}

func TestUDPWorkerExpiryCancelsAndDiscards(t *testing.T) {
	worker, id := testUDPWorker()
	t.Cleanup(func() { worker.Close() })
	conn, _ := worker.getConnection(id)
	packet := buf.New()
	packet.WriteString("expired packet")
	if err := conn.writer.WriteMultiBuffer(buf.MultiBuffer{packet}); err != nil {
		t.Fatal(err)
	}
	conn.lastActivityTime.Store(int32(time.Now().Unix() - 121))
	if err := worker.clean(); err != nil {
		t.Fatal(err)
	}
	if conn.ctx.Err() != context.Canceled || packet.Bytes() != nil || len(worker.activeConn) != 0 {
		t.Fatal("expired session retained context, packets or map entry")
	}
}

func TestUDPWorkerConcurrentCloseAndReplacement(t *testing.T) {
	worker, id := testUDPWorker()
	t.Cleanup(func() { worker.Close() })
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range 8 {
		wg.Go(func() {
			<-start
			for range 100 {
				conn, _ := worker.getConnection(id)
				if conn == nil {
					continue
				}
				packet := buf.New()
				packet.WriteString("racing packet")
				conn.writer.WriteMultiBuffer(buf.MultiBuffer{packet})
				conn.Close()
				worker.removeConn(id, conn)
			}
		})
	}
	wg.Go(func() { <-start; worker.Close() })
	close(start)
	wg.Wait()
	if len(worker.activeConn) != 0 || !worker.closed {
		t.Fatal("shutdown raced with a new UDP session")
	}
}

func TestUDPConnectionReaderContract(t *testing.T) {
	var conn any = &udpConn{}
	if _, ok := conn.(buf.Reader); !ok {
		t.Fatal("UDP connection lost packet reader")
	}
	if _, ok := conn.(buf.TimeoutReader); ok {
		t.Fatal("UDP connection must retain dispatcher-owned sniffing deadlines")
	}
}

func TestTransparentUDPSessionsKeepDestination(t *testing.T) {
	source := net.UDPDestination(net.IPAddress([]byte{192, 168, 1, 2}), 42001)
	first := net.UDPDestination(net.IPAddress([]byte{1, 1, 1, 1}), 53)
	second := net.UDPDestination(net.IPAddress([]byte{9, 9, 9, 9}), 53)
	otherPort := net.UDPDestination(first.Address, 443)
	worker := udpWorker{cone: true, stream: &internet.MemoryStreamConfig{
		SocketSettings: &internet.SocketConfig{Tproxy: internet.SocketConfig_TProxy},
	}}
	firstID := worker.connectionID(source, first)
	if firstID != worker.connectionID(source, first) {
		t.Fatal("same flow does not reuse its session")
	}
	for _, dest := range []net.Destination{second, otherPort} {
		if firstID == worker.connectionID(source, dest) {
			t.Fatalf("transparent session shared across destinations: %v", dest)
		}
	}
	if firstID.src != source || firstID.dest != first {
		t.Fatalf("incorrect flow key: %+v", firstID)
	}
}

func TestOrdinaryUDPConeSessionKeyUnchanged(t *testing.T) {
	source := net.UDPDestination(net.IPAddress([]byte{192, 168, 1, 2}), 42001)
	first := net.UDPDestination(net.IPAddress([]byte{1, 1, 1, 1}), 53)
	second := net.UDPDestination(net.IPAddress([]byte{9, 9, 9, 9}), 53)
	worker := udpWorker{cone: true}
	if worker.connectionID(source, first) != worker.connectionID(source, second) {
		t.Fatal("non-transparent full-cone behavior changed")
	}
	worker.cone = false
	if worker.connectionID(source, first) == worker.connectionID(source, second) {
		t.Fatal("non-cone sessions must keep their destination")
	}
	if got := worker.connectionID(source, net.Destination{}); got.src != source || got.dest.IsValid() {
		t.Fatalf("invalid original destination: %+v", got)
	}
}
