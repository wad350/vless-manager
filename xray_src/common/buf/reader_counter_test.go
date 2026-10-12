package buf

import (
	"bytes"
	"io"
	"net"
	"strconv"
	"testing"
	"time"

	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/transport/internet/stat"
)

type counterMultiConn struct {
	net.Conn
	mb          MultiBuffer
	err         error
	closed      bool
	interrupted bool
}

func (*counterMultiConn) Read([]byte) (int, error) { panic("byte-copy fallback used") }
func (c *counterMultiConn) ReadMultiBuffer() (MultiBuffer, error) {
	mb := c.mb
	c.mb = nil
	return mb, c.err
}
func (c *counterMultiConn) Close() error { c.closed = true; return nil }
func (c *counterMultiConn) Interrupt()   { c.interrupted = true }

type counterTimedConn struct {
	*counterMultiConn
	timeout time.Duration
}

func (c *counterTimedConn) ReadMultiBufferTimeout(timeout time.Duration) (MultiBuffer, error) {
	c.timeout = timeout
	return c.ReadMultiBuffer()
}

func TestStatsReaderPreservesMultiBuffer(t *testing.T) {
	for _, packet := range []bool{false, true} {
		for _, terminal := range []error{nil, io.EOF, io.ErrUnexpectedEOF} {
			var count testCounter
			payload := FromBytes([]byte("payload"))
			dest := xnet.UDPDestination(xnet.LocalHostIP, 443)
			payload.UDP = &dest
			conn := &counterMultiConn{mb: MultiBuffer{payload}, err: terminal}
			wrapper := &stat.CounterConnection{Connection: conn, ReadCounter: &count}
			reader := NewReader(wrapper)
			if packet {
				reader = NewPacketReader(wrapper)
			}
			if _, ok := reader.(TimeoutReader); ok {
				t.Fatal("reader invented timeout support")
			}
			mb, err := reader.ReadMultiBuffer()
			if len(mb) != 1 || mb[0] != payload || mb[0].UDP != &dest || err != terminal {
				t.Fatalf("buffer ownership, UDP destination or terminal error lost: %v, %v", mb, err)
			}
			if count.Value() != 7 {
				t.Fatalf("count = %d, want 7", count.Value())
			}
			ReleaseMulti(mb)
			_, _ = reader.ReadMultiBuffer()
			if count.Value() != 7 {
				t.Fatal("empty/terminal read counted twice")
			}
			reader.(*countedReader).Interrupt()
			if err := reader.(*countedReader).Close(); err != nil || !conn.closed || !conn.interrupted {
				t.Fatal("close/interrupt not forwarded")
			}
		}
	}
}

func TestStatsReaderNilCounterAndTimeout(t *testing.T) {
	conn := &counterMultiConn{}
	if got := NewReader(&stat.CounterConnection{Connection: conn}); got != conn {
		t.Fatal("nil counter should return the original multi-buffer reader")
	}
	var count testCounter
	timed := &counterTimedConn{counterMultiConn: &counterMultiConn{mb: MultiBuffer{FromBytes([]byte("ok"))}, err: io.EOF}}
	reader := NewReader(&stat.CounterConnection{Connection: timed, ReadCounter: &count})
	mb, err := reader.(TimeoutReader).ReadMultiBufferTimeout(17 * time.Millisecond)
	if err != io.EOF || count.Value() != 2 || timed.timeout != 17*time.Millisecond {
		t.Fatalf("timeout read lost accounting or arguments: %v, count %d, timeout %v", err, count.Value(), timed.timeout)
	}
	ReleaseMulti(mb)
}

func TestStatsReaderByteFallbackStillCounts(t *testing.T) {
	var count testCounter
	conn := &counterByteConn{Reader: bytes.NewReader([]byte("ordinary TCP"))}
	reader := NewReader(&stat.CounterConnection{Connection: conn, ReadCounter: &count})
	mb, err := reader.ReadMultiBuffer()
	if err != nil || count.Value() != 12 || string(mb[0].Bytes()) != "ordinary TCP" {
		t.Fatalf("byte fallback = %v, %v, count %d", mb, err, count.Value())
	}
	ReleaseMulti(mb)
}

type counterByteConn struct {
	net.Conn
	io.Reader
}

func (c *counterByteConn) Read(p []byte) (int, error) { return c.Reader.Read(p) }

type benchmarkCounterConn struct {
	net.Conn
	data []byte
}

func (c *benchmarkCounterConn) Read(p []byte) (int, error) { return copy(p, c.data), nil }
func (c *benchmarkCounterConn) ReadMultiBuffer() (MultiBuffer, error) {
	return MultiBuffer{FromBytes(c.data)}, nil
}

func BenchmarkStatsMultiBufferReader(b *testing.B) {
	for _, size := range []int{1200, Size} {
		for _, legacy := range []bool{false, true} {
			name := "owned"
			if legacy {
				name = "legacy_copy"
			}
			b.Run(name+"/"+strconv.Itoa(size), func(b *testing.B) {
				var count testCounter
				conn := &stat.CounterConnection{Connection: &benchmarkCounterConn{data: make([]byte, size)}, ReadCounter: &count}
				reader := NewReader(conn)
				if legacy {
					reader = &SingleReader{Reader: conn}
				}
				b.ReportAllocs()
				b.SetBytes(int64(size))
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					mb, _ := reader.ReadMultiBuffer()
					ReleaseMulti(mb)
				}
				b.StopTimer()
				if count.Value() != int64(size)*int64(b.N) {
					b.Fatal("incorrect benchmark accounting")
				}
			})
		}
	}
}
