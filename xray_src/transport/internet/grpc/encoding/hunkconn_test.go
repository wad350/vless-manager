package encoding

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/xtls/xray-core/common/buf"
)

type testHunkConn struct {
	hunks []*Hunk
}

type closableHunkConn struct{ testHunkConn }

func (*closableHunkConn) CloseSend() error { return nil }

func TestHunkCloseCancelsAndRejectsFurtherIO(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	h := NewHunkReadWriter(&closableHunkConn{}, cancel)
	if err := h.Close(); err != nil || ctx.Err() != context.Canceled {
		t.Fatalf("close did not cancel RPC: %v, %v", err, ctx.Err())
	}
	if _, err := h.Write([]byte("closed")); err != io.ErrClosedPipe {
		t.Fatalf("write after CloseSend = %v", err)
	}
	if _, err := h.ReadMultiBuffer(); err != io.EOF {
		t.Fatalf("read after CloseSend = %v", err)
	}
}

func (*testHunkConn) Context() context.Context  { return context.Background() }
func (*testHunkConn) Send(*Hunk) error          { return nil }
func (*testHunkConn) SendMsg(interface{}) error { return nil }
func (*testHunkConn) RecvMsg(interface{}) error { return nil }
func (c *testHunkConn) Recv() (*Hunk, error) {
	if len(c.hunks) == 0 {
		return nil, io.EOF
	}
	hunk := c.hunks[0]
	c.hunks = c.hunks[1:]
	return hunk, nil
}

func TestHunkReaderWriterDoesNotReplayConsumedPrefix(t *testing.T) {
	data := bytes.Repeat([]byte("x"), buf.Size)
	copy(data[:4], "head")
	h := NewHunkReadWriter(&testHunkConn{hunks: []*Hunk{{Data: data}}}, nil)
	prefix := make([]byte, 4)
	if n, err := h.Read(prefix); n != 4 || err != nil || string(prefix) != "head" {
		t.Fatalf("prefix = %q, %d, %v", prefix, n, err)
	}
	mb, err := h.ReadMultiBuffer()
	if err != nil {
		t.Fatal(err)
	}
	defer buf.ReleaseMulti(mb)
	if got := mb.Len(); got != int32(len(data)-len(prefix)) {
		t.Fatalf("remaining bytes = %d, want %d", got, len(data)-len(prefix))
	}
	if !bytes.Equal(mb[0].Bytes(), data[len(prefix):]) {
		t.Fatal("read repeated or changed the consumed prefix")
	}
	if &mb[0].Bytes()[0] != &data[len(prefix)] || h.buf != nil {
		t.Fatal("remaining hunk was copied or retained")
	}
}

func TestHunkReaderTransfersSmallAndLargePayloads(t *testing.T) {
	for _, size := range []int{0, 1, 1400, buf.Size, 3 * buf.Size} {
		data := bytes.Repeat([]byte{23}, size)
		h := NewHunkReadWriter(&testHunkConn{hunks: []*Hunk{{Data: data}, {Data: []byte("next")}}}, nil)
		mb, err := h.ReadMultiBuffer()
		if err != nil {
			t.Fatal(err)
		}
		if size == 0 {
			if len(mb) != 0 {
				t.Fatal("empty hunk allocated a buffer")
			}
		} else {
			if len(mb) != 1 || !bytes.Equal(mb[0].Bytes(), data) || &mb[0].Bytes()[0] != &data[0] || h.buf != nil {
				t.Fatalf("size %d: payload copied, retained or changed", size)
			}
		}
		buf.ReleaseMulti(mb)
		next, err := h.ReadMultiBuffer()
		if err != nil || string(next[0].Bytes()) != "next" {
			t.Fatalf("subsequent hunk = %v, %v", next, err)
		}
		buf.ReleaseMulti(next)
		if _, err := h.ReadMultiBuffer(); err != io.EOF {
			t.Fatalf("end of stream = %v", err)
		}
	}
}

type benchmarkHunkConn struct {
	testHunkConn
	data []byte
}

func (c *benchmarkHunkConn) Recv() (*Hunk, error) { return &Hunk{Data: c.data}, nil }

func BenchmarkHunkReadOwnership(b *testing.B) {
	for _, legacy := range []bool{false, true} {
		name := "owned"
		if legacy {
			name = "legacy_copy"
		}
		b.Run(name, func(b *testing.B) {
			h := NewHunkReadWriter(&benchmarkHunkConn{data: make([]byte, 1400)}, nil)
			b.SetBytes(1400)
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if legacy {
					buffer := buf.New()
					_, err := buffer.ReadFrom(h)
					if err != nil {
						b.Fatal(err)
					}
					buf.ReleaseMulti(buf.MultiBuffer{buffer})
				} else {
					mb, err := h.ReadMultiBuffer()
					if err != nil {
						b.Fatal(err)
					}
					buf.ReleaseMulti(mb)
				}
			}
		})
	}
}
