package encoding

import (
	"context"
	"io"
	"sync"

	"github.com/xtls/xray-core/common/buf"
	"github.com/xtls/xray-core/common/errors"
	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/net/cnc"
	"github.com/xtls/xray-core/common/signal/done"
)

type HunkConn interface {
	Context() context.Context
	Send(*Hunk) error
	Recv() (*Hunk, error)
	SendMsg(m interface{}) error
	RecvMsg(m interface{}) error
}

type StreamCloser interface {
	CloseSend() error
}

type HunkReaderWriter struct {
	hc     HunkConn
	cancel context.CancelFunc
	done   *done.Instance
	sendMu sync.Mutex

	buf   []byte
	index int
}

func NewHunkReadWriter(hc HunkConn, cancel context.CancelFunc) *HunkReaderWriter {
	return &HunkReaderWriter{hc: hc, cancel: cancel, done: done.New()}
}

func NewHunkConn(hc HunkConn, cancel context.CancelFunc, trustedXForwardedFor []string) net.Conn {
	rAddr := remoteAddrFromContext(hc.Context(), trustedXForwardedFor)
	lAddr := localAddrFromContext(hc.Context())
	wrc := NewHunkReadWriter(hc, cancel)
	return cnc.NewConnection(
		cnc.ConnectionInput(wrc),
		cnc.ConnectionOutput(wrc),
		cnc.ConnectionOnClose(wrc),
		cnc.ConnectionRemoteAddr(rAddr),
		cnc.ConnectionLocalAddr(lAddr),
	)
}

func (h *HunkReaderWriter) forceFetch() error {
	hunk, err := h.hc.Recv()
	if err != nil {
		if err == io.EOF {
			return err
		}

		return errors.New("failed to fetch hunk from gRPC tunnel").Base(err)
	}

	h.buf = hunk.Data
	h.index = 0

	return nil
}

func (h *HunkReaderWriter) Read(p []byte) (int, error) {
	if h.done.Done() {
		return 0, io.EOF
	}

	if h.index >= len(h.buf) {
		if err := h.forceFetch(); err != nil {
			return 0, err
		}
	}
	n := copy(p, h.buf[h.index:])
	h.index += n
	if h.index == len(h.buf) {
		if cap(h.buf) >= buf.Size {
			// No reader retains this payload after the ordinary Read copied it.
			buf.FromOwnedBytes(h.buf).Release()
		}
		h.buf, h.index = nil, 0
	}

	return n, nil
}

func (h *HunkReaderWriter) ReadMultiBuffer() (buf.MultiBuffer, error) {
	if h.done.Done() {
		return nil, io.EOF
	}
	if h.index >= len(h.buf) {
		if err := h.forceFetch(); err != nil {
			return nil, err
		}
	}

	if len(h.buf) == 0 {
		return nil, nil
	}
	var b *buf.Buffer
	if cap(h.buf) >= buf.Size {
		b = buf.FromOwnedBytes(h.buf)
		b.Advance(int32(h.index))
	} else {
		b = buf.FromBytes(h.buf[h.index:])
	}
	// Recv owns this protobuf payload; transfer it instead of copying it again.
	h.buf, h.index = nil, 0
	return buf.MultiBuffer{b}, nil
}

func (h *HunkReaderWriter) Write(buf []byte) (int, error) {
	if h.cancel == nil {
		h.sendMu.Lock()
		defer h.sendMu.Unlock()
	}
	if h.done.Done() {
		return 0, io.ErrClosedPipe
	}

	err := h.hc.Send(&Hunk{Data: buf[:]})
	if err != nil {
		return 0, errors.New("failed to send data over gRPC tunnel").Base(err)
	}
	return len(buf), nil
}

func (h *HunkReaderWriter) Close() error {
	_ = h.done.Close()
	if h.cancel != nil {
		h.cancel()
		return nil
	}
	if sc, match := h.hc.(StreamCloser); match {
		h.sendMu.Lock()
		defer h.sendMu.Unlock()
		return sc.CloseSend()
	}

	return nil
}
