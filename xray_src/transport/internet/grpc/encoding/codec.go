package encoding

import (
	"os"

	"github.com/xtls/xray-core/common/buf"
	"github.com/xtls/xray-core/common/bytespool"
	grpcencoding "google.golang.org/grpc/encoding"
	_ "google.golang.org/grpc/encoding/proto"
	"google.golang.org/grpc/mem"
	"google.golang.org/protobuf/encoding/protowire"
)

// TunnelCodec retains the proto wire format and scopes pooling to Xray tunnels.
// HunkReaderWriter transfers the resulting owned payload into buf.Buffer.
func TunnelCodec() grpcencoding.CodecV2 {
	fallback := grpcencoding.GetCodecV2("proto")
	if os.Getenv("XRAY_GRPC_BUFFER_POOL") == "off" {
		return fallback
	}
	return tunnelCodec{fallback: fallback}
}

type tunnelCodec struct{ fallback grpcencoding.CodecV2 }

func (c tunnelCodec) Name() string { return c.fallback.Name() }

func (c tunnelCodec) Marshal(v any) (mem.BufferSlice, error) { return c.fallback.Marshal(v) }

func (c tunnelCodec) Unmarshal(data mem.BufferSlice, v any) error {
	h, ok := v.(*Hunk)
	if !ok || h == nil {
		return c.fallback.Unmarshal(data, v)
	}
	wire := data.MaterializeToBuffer(mem.DefaultBufferPool())
	defer wire.Free()
	payload, ok := singleHunkPayload(wire.ReadOnlyData())
	if !ok {
		// Unknown fields, duplicate fields and malformed input keep upstream semantics.
		return c.fallback.Unmarshal(data, v)
	}
	h.Reset()
	h.Data = bytespool.Alloc(int32(max(buf.Size, len(payload))))[:len(payload)]
	copy(h.Data, payload)
	return nil
}

func singleHunkPayload(wire []byte) ([]byte, bool) {
	field, kind, n := protowire.ConsumeTag(wire)
	if n < 0 || field != 1 || kind != protowire.BytesType {
		return nil, false
	}
	payload, consumed := protowire.ConsumeBytes(wire[n:])
	return payload, consumed >= 0 && n+consumed == len(wire) && len(payload) > 0
}
