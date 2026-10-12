package encoding

import (
	"bytes"
	"io"
	"testing"

	"github.com/xtls/xray-core/common/buf"
)

type testMultiHunkConn struct {
	testHunkConn
	multi []*MultiHunk
}

func (*testMultiHunkConn) Send(*MultiHunk) error { return nil }
func (c *testMultiHunkConn) Recv() (*MultiHunk, error) {
	if len(c.multi) == 0 {
		return nil, io.EOF
	}
	hunk := c.multi[0]
	c.multi = c.multi[1:]
	return hunk, nil
}

func TestMultiHunkTransfersPayloadOwnership(t *testing.T) {
	data := [][]byte{nil, []byte("small"), bytes.Repeat([]byte{42}, buf.Size), bytes.Repeat([]byte{91}, 3*buf.Size)}
	h := NewMultiHunkReadWriter(&testMultiHunkConn{multi: []*MultiHunk{{Data: data}}}, nil)
	mb, err := h.ReadMultiBuffer()
	if err != nil || len(mb) != 3 || h.buf != nil {
		t.Fatalf("buffers = %v, error = %v, retained data = %v", mb, err, h.buf)
	}
	for i, b := range mb {
		if !bytes.Equal(b.Bytes(), data[i+1]) || &b.Bytes()[0] != &data[i+1][0] {
			t.Fatal("multi-hunk payload copied or changed")
		}
	}
	buf.ReleaseMulti(mb)
	if _, err := h.ReadMultiBuffer(); err != io.EOF {
		t.Fatalf("terminal error = %v", err)
	}
}
