package encoding

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/xtls/xray-core/common/buf"
	"google.golang.org/grpc/mem"
	"google.golang.org/protobuf/proto"
)

func releaseDecodedHunk(h *Hunk) {
	if cap(h.Data) >= buf.Size {
		buf.FromOwnedBytes(h.Data).Release()
	}
	h.Data = nil
}

func TestTunnelCodecCompatibility(t *testing.T) {
	for _, wire := range [][]byte{
		nil, {10, 0}, {10, 3, 1, 2, 3}, {10, 1, 7, 10, 1, 8},
		{16, 42, 10, 1, 7}, {10, 1, 7, 16, 42}, {8, 42},
		{27, 32, 1, 28}, {0}, {10, 128}, {10, 3, 1}, {15}, {128},
	} {
		t.Run(fmt.Sprintf("%x", wire), func(t *testing.T) { checkHunkCodec(t, wire) })
	}
	for _, size := range []int{1, 64, 1400, buf.Size, buf.Size + 1, 131073} {
		wire, err := proto.Marshal(&Hunk{Data: bytes.Repeat([]byte{0xa5}, size)})
		if err != nil {
			t.Fatal(err)
		}
		checkHunkCodec(t, wire)
	}
	codec := TunnelCodec()
	m := &MultiHunk{Data: [][]byte{{1, 2}, {3, 4}}}
	wire, err := codec.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	defer wire.Free()
	var decoded MultiHunk
	if err := codec.Unmarshal(wire, &decoded); err != nil || !proto.Equal(m, &decoded) {
		t.Fatalf("MultiHunk fallback: %v", err)
	}
}

func TestTunnelCodecOptOut(t *testing.T) {
	t.Setenv("XRAY_GRPC_BUFFER_POOL", "off")
	if _, pooled := TunnelCodec().(tunnelCodec); pooled {
		t.Fatal("pool opt-out ignored")
	}
	t.Setenv("XRAY_GRPC_BUFFER_POOL", "on")
	if _, pooled := TunnelCodec().(tunnelCodec); !pooled {
		t.Fatal("pool not enabled")
	}
}

func TestTunnelCodecFragmentedInputAndMarshal(t *testing.T) {
	want := &Hunk{Data: bytes.Repeat([]byte{0xa5}, 16384)}
	wire, err := proto.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	for _, split := range []int{1, 2, 3, 8192, len(wire) - 1} {
		var got Hunk
		input := mem.BufferSlice{mem.SliceBuffer(wire[:split]), mem.SliceBuffer(wire[split:])}
		if err := TunnelCodec().Unmarshal(input, &got); err != nil || !proto.Equal(&got, want) {
			t.Fatalf("fragmented input at %d: %v", split, err)
		}
		encoded, err := TunnelCodec().Marshal(&got)
		if err != nil || !bytes.Equal(encoded.Materialize(), wire) {
			t.Fatalf("marshal changed wire: %v", err)
		}
		encoded.Free()
		releaseDecodedHunk(&got)
	}
}

func checkHunkCodec(t testing.TB, wire []byte) {
	t.Helper()
	var want, got Hunk
	want.Data, got.Data = []byte{9}, []byte{9}
	errWant := proto.Unmarshal(wire, &want)
	errGot := TunnelCodec().Unmarshal(mem.BufferSlice{mem.SliceBuffer(wire)}, &got)
	defer releaseDecodedHunk(&got)
	if (errGot == nil) != (errWant == nil) {
		t.Fatalf("error mismatch: %v / %v", errGot, errWant)
	}
	if errWant == nil && !proto.Equal(&got, &want) {
		t.Fatalf("decoded Hunk differs for %x", wire)
	}
	if _, pooled := singleHunkPayload(wire); pooled && errGot == nil {
		if cap(got.Data) < buf.Size {
			t.Fatal("payload was not pooled")
		}
		original := append([]byte(nil), got.Data...)
		for i := range wire {
			wire[i] ^= 0xff
		}
		if !bytes.Equal(got.Data, original) {
			t.Fatal("payload aliases borrowed gRPC input")
		}
	}
}

func FuzzTunnelCodecCompatibility(f *testing.F) {
	for _, seed := range [][]byte{{10, 3, 1, 2, 3}, {10, 0}, {10, 1, 7, 16, 42}, {10, 1, 7, 10, 1, 8}, {27, 32, 1, 28}, {10, 128}} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, wire []byte) { checkHunkCodec(t, wire) })
}

func BenchmarkTunnelDecode(b *testing.B) {
	for _, size := range []int{64, 1400, buf.Size, 16384} {
		wire, _ := proto.Marshal(&Hunk{Data: bytes.Repeat([]byte{0xa5}, size)})
		for _, pooled := range []bool{false, true} {
			b.Run(fmt.Sprintf("pooled=%t/%d", pooled, size), func(b *testing.B) {
				codec := TunnelCodec()
				if !pooled {
					codec = codec.(tunnelCodec).fallback
				}
				b.SetBytes(int64(size))
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					var h Hunk
					if err := codec.Unmarshal(mem.BufferSlice{mem.SliceBuffer(wire)}, &h); err != nil {
						b.Fatal(err)
					}
					if cap(h.Data) >= buf.Size {
						buf.FromOwnedBytes(h.Data).Release()
					} else {
						buf.FromBytes(h.Data).Release()
					}
				}
			})
		}
	}
}
