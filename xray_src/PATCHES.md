# Embedded Runtime Patches

Upstream: https://github.com/XTLS/Xray-core
Tag: v26.9.9 (prerelease)
Commit: 52a412d9e2f5c2a5142b1b4e2ab3771dacb8b120
Go module: v1.260327.1-0.20260908222543-52a412d9e2f5

This is the upstream module source with local fixes, not an unmodified
official Xray build. Keep the upstream LICENSE and these changes when updating.

1. `common/buf/writer.go`: account for `io.Writer.Write` calls on
   `BufferToBytesWriter`, including the initial buffered VLESS request. The
   inherited Write method bypassed the upload counter.
2. `transport/internet/grpc/encoding/hunkconn.go`: do not return the entire
   hunk after its prefix was consumed via Read. Otherwise switching from Read
   (VLESS response header) to ReadMultiBuffer replays that prefix into the payload.
3. `core/xray.go`, `transport/internet/dialer.go`,
   `app/proxyman/outbound/handler.go`, `proxy/freedom/freedom.go`: bind DNS and
   outbound-manager references to each instance context, instead of overwriting
   process-global state when concurrent probe instances are constructed.
4. `transport/internet/grpc/dial.go`: carry those features through gRPC's
   internal dial context and close cached clients when an instance stops.

Focused tests are alongside the changed packages. End-to-end transport tests,
including route accounting, live in the manager.
