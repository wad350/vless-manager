package encoding

import (
	"bytes"
	"io"
	"testing"

	"github.com/xtls/xray-core/common/buf"
	"github.com/xtls/xray-core/common/errors"
)

func TestLengthPacketPayloadOwnership(t *testing.T) {
	for _, tc := range []struct {
		name   string
		length int32
		bytes  int
		err    error
	}{
		{"empty", 0, 0, nil},
		{"small_complete", 5, 5, nil},
		{"large_complete", buf.Size + 7, buf.Size + 7, nil},
		{"maximum_complete", 65535, 65535, nil},
		{"small_truncated", 5, 3, io.ErrUnexpectedEOF},
		{"large_truncated", buf.Size + 7, buf.Size + 2, io.ErrUnexpectedEOF},
		{"large_missing_fragment", buf.Size + 7, buf.Size, io.EOF},
		{"third_fragment_truncated", 2*buf.Size + 7, 2*buf.Size + 2, io.ErrUnexpectedEOF},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := bytes.Repeat([]byte{0xa5}, tc.bytes)
			var allocated []*buf.Buffer
			newBuffer := func() *buf.Buffer {
				b := buf.New()
				allocated = append(allocated, b)
				return b
			}
			t.Cleanup(func() {
				for _, b := range allocated {
					b.Release()
				}
			})
			mb, err := readLengthPacketPayload(bytes.NewReader(payload), tc.length, newBuffer)
			if errors.Cause(err) != tc.err {
				t.Fatalf("error = %v, want %v", err, tc.err)
			}
			if tc.err != nil {
				if mb != nil {
					t.Fatalf("truncated payload returned buffers: %v", mb)
				}
				for i, b := range allocated {
					if b.Bytes() != nil {
						t.Errorf("buffer %d retained %d bytes after terminal read error", i, b.Len())
					}
				}
				return
			}
			if !bytes.Equal([]byte(mb.String()), payload) {
				t.Fatal("complete payload changed")
			}
			if len(mb) != len(allocated) {
				t.Fatal("allocated buffers not transferred to caller")
			}
			for i, b := range allocated {
				if mb[i] != b || b.Bytes() == nil {
					t.Fatal("successful read lost buffer ownership")
				}
			}
			buf.ReleaseMulti(mb)
		})
	}
}

func TestLengthPacketReaderWireLengths(t *testing.T) {
	for _, size := range []int{0, 5, buf.Size + 7, 65535} {
		payload := bytes.Repeat([]byte{0xa5}, size)
		wire := append([]byte{byte(size >> 8), byte(size)}, payload...)
		mb, err := NewLengthPacketReader(bytes.NewReader(wire)).ReadMultiBuffer()
		if err != nil || !bytes.Equal([]byte(mb.String()), payload) {
			buf.ReleaseMulti(mb)
			t.Fatalf("length %d: read error %v", size, err)
		}
		buf.ReleaseMulti(mb)
	}
}
