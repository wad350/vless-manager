package tun

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xtls/xray-core/common/buf"
	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/waiter"
)

type batchEndpoint struct {
	tcpip.Endpoint
	write func(tcpip.Payloader, tcpip.WriteOptions) (int64, tcpip.Error)
}

func (e *batchEndpoint) Write(p tcpip.Payloader, opts tcpip.WriteOptions) (int64, tcpip.Error) {
	return e.write(p, opts)
}
func (e *batchEndpoint) Close() {}

func batchBuffers(p []byte) buf.MultiBuffer { return buf.MergeBytes(nil, p) }

func assertReleased(t *testing.T, mb buf.MultiBuffer) {
	t.Helper()
	for _, b := range mb {
		if b != nil {
			t.Fatal("input buffer reference retained")
		}
	}
}

func TestTCPBatchPartialWritesAndAccounting(t *testing.T) {
	want := bytes.Repeat([]byte{1, 2, 3, 4, 5}, 20000)
	var output bytes.Buffer
	maxLen, calls := 0, 0
	ep := &batchEndpoint{write: func(p tcpip.Payloader, opts tcpip.WriteOptions) (int64, tcpip.Error) {
		if !opts.Atomic {
			t.Fatal("write must not discard consumed payload")
		}
		maxLen = max(maxLen, p.Len())
		calls++
		n, err := io.CopyN(&output, p, int64(min(p.Len(), 1234)))
		if err != nil {
			t.Fatal(err)
		}
		return n, nil
	}}
	c := newGVisorTCPConn(new(waiter.Queue), ep)
	count := new(testCounter)
	mb := batchBuffers(want)
	if err := (&tcpBatchWriter{conn: c, counter: count}).WriteMultiBuffer(mb); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(want, output.Bytes()) || count.Value() != int64(len(want)) {
		t.Fatalf("output=%d counter=%d", output.Len(), count.Value())
	}
	if maxLen != tcpWriteBatchSize || calls < 2 {
		t.Fatalf("max=%d calls=%d", maxLen, calls)
	}
	assertReleased(t, mb)
}

func TestTCPBatchErrorCountsOnlyAcceptedBytes(t *testing.T) {
	calls := 0
	ep := &batchEndpoint{write: func(p tcpip.Payloader, _ tcpip.WriteOptions) (int64, tcpip.Error) {
		calls++
		if calls == 2 {
			return 0, &tcpip.ErrClosedForSend{}
		}
		n, _ := io.CopyN(io.Discard, p, 517)
		return n, nil
	}}
	count := new(testCounter)
	mb := batchBuffers(make([]byte, 40000))
	err := (&tcpBatchWriter{conn: newGVisorTCPConn(new(waiter.Queue), ep), counter: count}).WriteMultiBuffer(mb)
	if err == nil || count.Value() != 517 {
		t.Fatalf("err=%v count=%d", err, count.Value())
	}
	assertReleased(t, mb)
}

func TestTCPBatchMissedWakeup(t *testing.T) {
	wq := new(waiter.Queue)
	calls := 0
	ep := &batchEndpoint{write: func(p tcpip.Payloader, _ tcpip.WriteOptions) (int64, tcpip.Error) {
		calls++
		if calls == 1 {
			wq.Notify(waiter.WritableEvents)
			return 0, &tcpip.ErrWouldBlock{}
		}
		n, _ := io.Copy(io.Discard, p)
		return n, nil
	}}
	c := newGVisorTCPConn(wq, ep)
	_ = c.SetWriteDeadline(time.Now().Add(time.Second))
	n, err := c.writeMultiBuffer(batchBuffers([]byte("wake")))
	if n != 4 || err != nil || calls != 2 {
		t.Fatalf("write=%d err=%v calls=%d", n, err, calls)
	}
}

func TestTCPBatchBlockedDeadlineAndClose(t *testing.T) {
	for _, action := range []string{"deadline", "close", "writable"} {
		t.Run(action, func(t *testing.T) {
			wq := new(waiter.Queue)
			mu := new(sync.Mutex)
			calls, writable := 0, false
			blocked := make(chan struct{})
			ep := &batchEndpoint{write: func(p tcpip.Payloader, _ tcpip.WriteOptions) (int64, tcpip.Error) {
				mu.Lock()
				defer mu.Unlock()
				calls++
				if !writable {
					if calls == 2 {
						close(blocked)
					}
					return 0, &tcpip.ErrWouldBlock{}
				}
				n, _ := io.Copy(io.Discard, p)
				return n, nil
			}}
			c := newGVisorTCPConn(wq, ep)
			mb := batchBuffers([]byte("test"))
			done := make(chan error, 1)
			go func() { _, err := c.writeMultiBuffer(mb); done <- err }()
			select {
			case <-blocked:
			case <-time.After(time.Second):
				t.Fatal("not blocked")
			}
			switch action {
			case "deadline":
				_ = c.SetWriteDeadline(time.Now().Add(-time.Second))
			case "close":
				_ = c.Close()
			case "writable":
				mu.Lock()
				writable = true
				mu.Unlock()
				wq.Notify(waiter.WritableEvents)
			}
			select {
			case err := <-done:
				if action == "deadline" && !errors.Is(err, os.ErrDeadlineExceeded) {
					t.Fatal(err)
				}
				if action == "close" && !errors.Is(err, net.ErrClosed) {
					t.Fatal(err)
				}
				if action == "writable" && err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				_ = c.Close()
				t.Fatal("blocked write did not wake")
			}
			assertReleased(t, mb)
		})
	}
}

func TestTCPBatchExpiredDeadlineEmptyAndNoProgress(t *testing.T) {
	ep := &batchEndpoint{write: func(tcpip.Payloader, tcpip.WriteOptions) (int64, tcpip.Error) { return 0, nil }}
	c := newGVisorTCPConn(new(waiter.Queue), ep)
	if n, err := c.writeMultiBuffer(nil); n != 0 || err != nil {
		t.Fatalf("empty=%d,%v", n, err)
	}
	mb := batchBuffers([]byte("test"))
	if _, err := c.writeMultiBuffer(mb); !errors.Is(err, io.ErrNoProgress) {
		t.Fatal(err)
	}
	assertReleased(t, mb)
	_ = c.SetDeadline(time.Now().Add(-time.Second))
	if _, err := c.writeMultiBuffer(batchBuffers([]byte("test"))); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatal(err)
	}
}

func TestTCPBatchDeadlineExtensionAndReset(t *testing.T) {
	c := newGVisorTCPConn(new(waiter.Queue), &batchEndpoint{})
	_ = c.SetWriteDeadline(time.Now().Add(100 * time.Millisecond))
	writable := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- c.waitWritable(writable) }()
	_ = c.SetWriteDeadline(time.Now().Add(time.Second))
	time.Sleep(150 * time.Millisecond)
	select {
	case err := <-done:
		t.Fatalf("extended deadline expired: %v", err)
	default:
	}
	_ = c.SetWriteDeadline(time.Time{})
	close(writable)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("deadline reset blocked")
	}
}

func TestTCPBufferRanges(t *testing.T) {
	for _, limit := range []int{0, 128 << 10} {
		ep := channel.New(1, 1500, "")
		s, err := createStackWithBufferLimit(ep, limit)
		if err != nil {
			t.Fatal(err)
		}
		var rx tcpip.TCPReceiveBufferSizeRangeOption
		var tx tcpip.TCPSendBufferSizeRangeOption
		if err := s.TransportProtocolOption(tcp.ProtocolNumber, &rx); err != nil {
			t.Fatal(err)
		}
		if err := s.TransportProtocolOption(tcp.ProtocolNumber, &tx); err != nil {
			t.Fatal(err)
		}
		wantRX, wantTX := tcpRXBufDefSize, tcpTXBufDefSize
		maxRX, maxTX := tcpRXBufMaxSize, tcpTXBufMaxSize
		if limit != 0 {
			wantRX = limit
			wantTX = limit
			maxRX = 4 * limit
			maxTX = 4 * limit
		}
		if rx.Min != tcpRXBufMinSize || tx.Min != tcpTXBufMinSize || rx.Default != wantRX || tx.Default != wantTX || rx.Max != maxRX || tx.Max != maxTX {
			t.Fatalf("limit=%d rx=%+v tx=%+v", limit, rx, tx)
		}
		s.Destroy()
	}
}

// Two real netstacks with no kernel offloads or external networking. This
// exercises segmentation, retransmission ownership, backpressure and FIN.
func tcpBatchPair(t testing.TB, bufferLimit int, ipv6 bool) (*gvisorTCPConn, *gonet.TCPConn, *int64) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	a, b := channel.New(256, 1500, ""), channel.New(256, 1500, "")
	sa, err := createStackWithBufferLimit(a, bufferLimit)
	if err != nil {
		t.Fatal(err)
	}
	sb, err := createStackWithBufferLimit(b, bufferLimit)
	if err != nil {
		t.Fatal(err)
	}
	protocol := header.IPv4ProtocolNumber
	var peer tcpip.Address
	for i, s := range []*stack.Stack{sa, sb} {
		addr := tcpip.AddrFrom4([4]byte{10, 0, 0, byte(i + 1)})
		if ipv6 {
			protocol = header.IPv6ProtocolNumber
			addr = tcpip.AddrFrom16([16]byte{0xfd, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, byte(i + 1)})
		}
		peer = addr
		if err := s.AddProtocolAddress(defaultNIC, tcpip.ProtocolAddress{Protocol: protocol, AddressWithPrefix: addr.WithPrefix()}, stack.AddressProperties{}); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	packets := new(int64)
	bridge := func(src, dst *channel.Endpoint, count bool) {
		defer wg.Done()
		for {
			pkt := src.ReadContext(ctx)
			if pkt == nil {
				return
			}
			if count {
				atomic.AddInt64(packets, 1)
			}
			// Strip outgoing header metadata before injecting a wire packet.
			wire := stackPacket(pkt)
			dst.InjectInbound(pkt.NetworkProtocolNumber, wire)
			wire.DecRef()
			pkt.DecRef()
		}
	}
	wg.Add(2)
	go bridge(a, b, false)
	go bridge(b, a, true)
	t.Cleanup(func() {
		cancel()
		wg.Wait()
		sa.Close()
		sb.Close()
		sa.Wait()
		sb.Wait()
		a.Close()
		b.Close()
	})
	accepted := make(chan *gvisorTCPConn, 1)
	f := tcp.NewForwarder(sb, 0, 16, func(r *tcp.ForwarderRequest) {
		var wq waiter.Queue
		ep, err := r.CreateEndpoint(&wq)
		if err != nil {
			r.Complete(true)
			return
		}
		r.Complete(false)
		accepted <- newGVisorTCPConn(&wq, ep)
	})
	sb.SetTransportProtocolHandler(tcp.ProtocolNumber, f.HandlePacket)
	client, err := gonet.DialContextTCP(ctx, sa, tcpip.FullAddress{NIC: defaultNIC, Addr: peer, Port: 443}, protocol)
	if err != nil {
		t.Fatal(err)
	}
	var server *gvisorTCPConn
	select {
	case server = <-accepted:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	_ = client.SetDeadline(time.Now().Add(8 * time.Second))
	_ = server.SetDeadline(time.Now().Add(8 * time.Second))
	t.Cleanup(func() { server.Close(); client.Close() })
	return server, client, packets
}

func stackPacket(pkt *stack.PacketBuffer) *stack.PacketBuffer {
	view := pkt.ToView()
	return stack.NewPacketBuffer(stack.PacketBufferOptions{Payload: buffer.MakeWithView(view)})
}

func TestTCPBatchRealStackConcurrentWritesAndFIN(t *testing.T) {
	for _, ipv6 := range []bool{false, true} {
		name := "IPv4"
		if ipv6 {
			name = "IPv6"
		}
		t.Run(name, func(t *testing.T) { testTCPBatchConcurrentWrites(t, ipv6) })
	}
}

func testTCPBatchConcurrentWrites(t *testing.T, ipv6 bool) {
	server, client, _ := tcpBatchPair(t, 128<<10, ipv6)
	const workers, size = 4, 65536
	want := bytes.Repeat([]byte("payload!"), size/8)
	errCh := make(chan error, workers+1)
	go func() {
		got, err := io.ReadAll(client)
		if err == nil && !bytes.Equal(got, bytes.Repeat(want, workers)) {
			err = errors.New("TCP payload corrupted")
		}
		errCh <- err
	}()
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := server.writeMultiBuffer(batchBuffers(want)); errCh <- err }()
	}
	wg.Wait()
	if err := server.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < workers+1; i++ {
		if err := <-errCh; err != nil {
			t.Fatal(err)
		}
	}
}

func BenchmarkTCPBatchRealStack(b *testing.B) {
	for _, variant := range []struct {
		batch bool
		limit int
	}{{false, 0}, {true, 0}, {false, 128 << 10}, {true, 128 << 10}} {
		batch := variant.batch
		name := "legacy"
		if batch {
			name = "batch"
		}
		if variant.limit != 0 {
			name += "_bounded"
		}
		b.Run(name, func(b *testing.B) {
			server, client, packets := tcpBatchPair(b, variant.limit, false)
			want := make([]byte, 65536)
			done := make(chan error, 1)
			go func() { _, err := io.CopyN(io.Discard, client, int64(b.N)*int64(len(want))); done <- err }()
			b.SetBytes(int64(len(want)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				mb := batchBuffers(want)
				if batch {
					if _, err := server.writeMultiBuffer(mb); err != nil {
						b.Fatal(err)
					}
				} else {
					for _, p := range mb {
						if _, err := server.TCPConn.Write(p.Bytes()); err != nil {
							b.Fatal(err)
						}
					}
					buf.ReleaseMulti(mb)
				}
			}
			if err := <-done; err != nil {
				b.Fatal(err)
			}
			b.ReportMetric(float64(atomic.LoadInt64(packets))/float64(b.N), "packets/op")
		})
	}
}
