package tun

import (
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"github.com/xtls/xray-core/common/buf"
	"github.com/xtls/xray-core/features/stats"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/waiter"
)

const tcpWriteBatchSize = 32 << 10

// Feed existing buffers to netstack together, without a coalescing buffer or
// waiting for more input. Keep reads and TCP lifecycle in the gonet adapter.
type gvisorTCPConn struct {
	*gonet.TCPConn
	ep              tcpip.Endpoint
	wq              *waiter.Queue
	writeMu         sync.Mutex
	deadlineMu      sync.Mutex
	writeDeadline   time.Time
	deadlineChanged chan struct{}
	closed          chan struct{}
	closeOnce       sync.Once
}

func newGVisorTCPConn(wq *waiter.Queue, ep tcpip.Endpoint) *gvisorTCPConn {
	return &gvisorTCPConn{
		TCPConn: gonet.NewTCPConn(wq, ep), ep: ep, wq: wq,
		deadlineChanged: make(chan struct{}), closed: make(chan struct{}),
	}
}

func (c *gvisorTCPConn) Write(p []byte) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.TCPConn.Write(p)
}

func (c *gvisorTCPConn) SetWriteDeadline(t time.Time) error {
	c.deadlineMu.Lock()
	defer c.deadlineMu.Unlock()
	if err := c.TCPConn.SetWriteDeadline(t); err != nil {
		return err
	}
	c.writeDeadline = t
	close(c.deadlineChanged)
	c.deadlineChanged = make(chan struct{})
	return nil
}

func (c *gvisorTCPConn) SetDeadline(t time.Time) error {
	if err := c.TCPConn.SetReadDeadline(t); err != nil {
		return err
	}
	return c.SetWriteDeadline(t)
}

func (c *gvisorTCPConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return c.TCPConn.Close()
}

func tcpWriteError(err error) error {
	return &net.OpError{Op: "write", Net: "tcp", Err: err}
}

func (c *gvisorTCPConn) deadline() (time.Time, <-chan struct{}) {
	c.deadlineMu.Lock()
	defer c.deadlineMu.Unlock()
	return c.writeDeadline, c.deadlineChanged
}

func (c *gvisorTCPConn) waitWritable(ch <-chan struct{}) error {
	for {
		d, changed := c.deadline()
		var timer *time.Timer
		var timeout <-chan time.Time
		if !d.IsZero() {
			if time.Until(d) <= 0 {
				return tcpWriteError(os.ErrDeadlineExceeded)
			}
			timer = time.NewTimer(time.Until(d))
			timeout = timer.C
		}
		var err error
		retry := false
		select {
		case <-c.closed:
			err = tcpWriteError(net.ErrClosed)
		case <-timeout:
			retry = true // Recheck in case the deadline changed concurrently.
		case <-changed:
			retry = true
		case <-ch:
		}
		if timer != nil {
			timer.Stop()
		}
		if !retry {
			return err
		}
	}
}

func (c *gvisorTCPConn) writeMultiBuffer(mb buf.MultiBuffer) (int64, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	p := tcpPayload{buffers: mb}
	defer func() { buf.ReleaseMulti(p.buffers) }()
	for _, b := range mb {
		p.remaining += int(b.Len())
	}
	var written int64
	var entry waiter.Entry
	var ch <-chan struct{}
	for p.remaining > 0 {
		select {
		case <-c.closed:
			return written, tcpWriteError(net.ErrClosed)
		default:
		}
		if d, _ := c.deadline(); !d.IsZero() && time.Until(d) <= 0 {
			return written, tcpWriteError(os.ErrDeadlineExceeded)
		}
		p.limit = min(p.remaining, tcpWriteBatchSize)
		// Atomic prevents netstack from consuming then discarding bytes when
		// another socket operation changes the available send window.
		n, err := c.ep.Write(&p, tcpip.WriteOptions{Atomic: true})
		written += n
		switch err.(type) {
		case nil:
			if n == 0 {
				return written, tcpWriteError(io.ErrNoProgress)
			}
		case *tcpip.ErrWouldBlock:
			if ch == nil {
				entry, ch = waiter.NewChannelEntry(waiter.WritableEvents)
				c.wq.EventRegister(&entry)
				defer c.wq.EventUnregister(&entry)
				continue // Retry after registration to avoid a missed wakeup.
			}
			if err := c.waitWritable(ch); err != nil {
				return written, err
			}
		default:
			return written, tcpWriteError(errors.New(err.String()))
		}
	}
	return written, nil
}

type tcpPayload struct {
	buffers   buf.MultiBuffer
	remaining int
	limit     int
}

func (p *tcpPayload) Len() int { return p.limit }

func (p *tcpPayload) Read(dst []byte) (int, error) {
	if len(dst) == 0 {
		return 0, nil
	}
	if p.limit == 0 {
		return 0, io.EOF
	}
	dst = dst[:min(len(dst), p.limit)]
	var n int
	p.buffers, n = buf.SplitBytes(p.buffers, dst)
	p.remaining -= n
	p.limit -= n
	return n, nil
}

type tcpBatchWriter struct {
	conn    *gvisorTCPConn
	counter stats.Counter
}

func (w *tcpBatchWriter) WriteMultiBuffer(mb buf.MultiBuffer) error {
	n, err := w.conn.writeMultiBuffer(mb)
	if w.counter != nil {
		w.counter.Add(n)
	}
	return err
}
