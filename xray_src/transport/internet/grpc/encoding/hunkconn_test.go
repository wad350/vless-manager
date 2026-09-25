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
}
