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
5. `proxy/tun/stack_gvisor.go`: copy UDP payloads directly from the original
   packet. The old intermediate clone was never DecRef'd, retaining buffer
   references and bypassing the packet pool on every inbound datagram.
6. `proxy/tun/udp_fullcone.go`: wrap the queue-owned payload with
   `buf.FromBytes` rather than copying it into a second 8 KiB buffer. Store
   the destination in the queued packet to avoid a separate allocation.
7. `transport/internet/tls/chacha20_fingerprint.go`, `tls.go`: opt-in
   `randomized-chacha20` fingerprint. Select an unmodified randomized uTLS
   ClientHello preferring ChaCha20, with TLS 1.2 minimum, TLS 1.3 and AES
   fallback retained. Browser presets and certificate verification are unchanged.
   Always advertise and send the ML-KEM/X25519 hybrid share before X25519,
   as required by modern REALITY; randomized share omissions are not accepted.
8. `common/buf/io.go`, `reader_counter.go`: preserve `ReadMultiBuffer` behind
   stats-wrapped connections rather than copying through `net.Conn.Read`.
   Count each returned payload exactly once, including data returned with a
   terminal error; preserve UDP destinations, timeouts, close and interruption.
   Ordinary byte-stream readers keep the original fallback. Write accounting
   and partial-write semantics are unchanged.
9. `transport/internet/grpc/encoding/hunkconn.go`, `multiconn.go`: transfer
   protobuf-owned payloads, including small messages and partially consumed
   hunks, without copying them into another 8 KiB buffer. Clear transport-held
   references on transfer and do not replay a consumed prefix. Standard-sized
   buffers retain the existing pooling behavior.
10. `proxy/tun/tcp_batch.go`, `handler.go`, `stack_gvisor.go`: experimental
    TCP payloader batches existing MultiBuffers into at most 32 KiB per
    netstack write, without a separate coalescing buffer. Serialize writes,
    handle writable notifications, updated deadlines and close, release input
    ownership on all paths and count only accepted bytes. This adapter is
    opt-in with `VLESS_MANAGER_TUN_TCP_BATCH=1`; ordinary gonet remains default.
11. `proxy/tun/stack_gvisor.go`: optional 128 KiB TCP send/receive defaults,
    with 512 KiB autotuning maxima (`VLESS_MANAGER_TUN_TCP_BUFFER=128k`).
    Minimum sizes, congestion control, SACK, routes, UDP and ICMP are unchanged.
    Defaults remain upstream-sized: local MIPS stack tests became more stable,
    but live gRPC tests did not establish a reliable throughput improvement.

Focused tests are alongside the changed packages. End-to-end transport tests,
including route accounting, live in the manager.

12. `infra/conf/tun.go`, `wireguard.go`, `xray.go`: optional upstream TUN and
    WireGuard config registration now requires `xray_tun` / `xray_wireguard`
    build tags. Neither is a manager feature. This prevents the config parser
    from indirectly linking gVisor into the native TCP/UDP TPROXY manager.
    The old TUN experiments above remain in the source tree but are not built
    into the manager. Full upstream builds needing these protocols must pass
    the tags explicitly.
13. `app/proxyman/inbound/worker.go`: native TPROXY UDP session keys include
    the original destination, even when ordinary UDP uses full-cone sessions.
    Otherwise a reused client port can send a query to the previous resolver
    (VLESS UDP/53 has a fixed destination) or retain the wrong routing decision.
    SOCKS and other non-TPROXY full-cone behavior is unchanged.
14. `common/buf/io.go`: replace a sleeping goroutine per sniffing deadline
    with a stopped Go timer. Preserve the upstream pending-read ownership,
    timeout results and byte accounting. An attempted direct timeout-reader
    delegation was removed while live UDP stress results were unstable in
    both old and new builds; UDP connections keep their original reader.
15. `common/protocol/quic/sniff.go`: reject non-QUIC headers and unsupported
    versions before acquiring 32 KiB handshake scratch buffers. Supported
    Initial packets retain the upstream parser and domain-sniffing behavior.
16. `transport/internet/udp/batch_linux.go`, `hub.go`: receive up to eight
    datagrams with Linux recvmmsg for plain transparent UDP sockets, including
    Go's IPv6 dual-stack wildcard listeners receiving IPv4 traffic.
    Each datagram keeps its own source, original destination and buffer owner.
    No deliberate wait for a full batch; truncated payload/control data is
    dropped. Unsupported kernels fall back to single-packet reads. Set
    `XRAY_TPROXY_UDP_BATCH=off` to disable; wrapped, non-transparent and
    non-Linux sockets retain their existing receive path.
17. `app/proxyman/inbound/worker.go`: publish UDP sessions with their cancel
    function already initialized. Expiry and shutdown cancel active sessions
    and interrupt their packet pipes, releasing queued buffers immediately.
    Completion removes a map entry only if it still owns that session;
    closed workers reject queued callbacks instead of creating new flows.
    Session keys, inactivity timeouts and active-flow routing are unchanged.
18. `common/buf/buffer.go`, UDP hub readers: reserve receive-only storage
    without clearing all 8 KiB before each syscall. Shrink to the actual
    initialized length before publishing the packet. Ordinary Extend/Resize
    still clear newly visible bytes. Both Linux batch and single receive
    paths discard payload/control truncation. Tests cover poisoned tails,
    varying lengths and oversized datagrams in both socket families/modes.
19. `transport/internet/grpc/encoding/codec.go`, `dial.go`, `hub.go`: a
    tunnel-scoped protobuf codec copies ordinary single-field Hunk messages
    into exclusively owned size-aware pooled buffers. Google protowire parses
    the field; borrowed gRPC buffers are never retained. Unknown, duplicate,
    empty and malformed fields and MultiHunk retain the upstream codec.
    Marshal and the on-wire content type remain standard protobuf. Ordinary
    Read releases fully consumed payloads; MultiBuffer transfers ownership.
    `XRAY_GRPC_BUFFER_POOL=off` restores the standard decoder. Compatibility,
    fragmentation, malformed-input fuzzing, mixed reads and concurrent real
    gRPC tests cover both codecs. MIPS microbenchmarks are not VPN speeds.
20. `common/protocol/udp/diagnostics.go`, UDP hub/worker and dokodemo reply
    writers: opt-in packet counters expose received/dispatched packets,
    hub overflow, truncation, emitted replies and write errors. The manager's
    authenticated RAM-only diagnostics enables them; normal service startup
    leaves them disabled. Counters use 32-bit atomics on MIPS and snapshots
    are modulo 2^32. They do not add logging, retry packets or change routing.
    A dispatched packet has been read from the inbound pipe, not necessarily
    delivered by the remote relay; these counters cannot prove remote delivery.
    Internal pipe overflow and VLESS body-writer acceptance/response counters
    distinguish local queue loss from replies missing after the VLESS boundary.
    Body-writer acceptance is not proof of remote delivery.
21. `transport/pipe/impl.go`: ready timed reads consume available data without
    creating a timer. Empty reads retain bounded waits and wakeup semantics.
    A nil-by-default overflow observer counts packets before ownership release.
22. UDP inactivity uses native 32-bit atomics rather than the MIPS runtime's
    locked 64-bit operations. Modular epoch subtraction preserves the existing
    two-minute expiry across epoch wrap; timeout and cleanup policy are unchanged.
23. gRPC client tunnels own their RPC cancellation context. Full connection
    close cancels both directions instead of only half-closing the send side.
    It never races CloseSend with an active Send. Wrappers without a cancel
    function serialize Send and CloseSend. Closed wrappers reject further I/O.
    Real gRPC quota/repeated-close and mixed ownership tests exercise cleanup.
24. `transport/pipe`, UDP worker: optional one-time scheduler yield before
    overflow discard gives ready readers a chance to drain a burst without
    increasing the 16 KiB queue or waiting for downstream I/O. It is disabled
    by default; `XRAY_UDP_YIELD_OVERFLOW=on` enables the experiment. Mac Hysteria2
    A/B runs passed but did not establish a repeatable throughput gain, so it
    is not enabled in normal service startup.
25. The embedded manager also registers upstream `tagged/taggedimpl`, which
    Observatory needs to probe grouped-profile members through named outbounds.
    Omitting it leaves `tagged.Dialer` nil and crashes the process when a grouped
    profile starts. A real two-member VLESS/gRPC integration test exercises the
    background probes instead of only validating the generated JSON.
26. Observatory publishes immutable status slices/rows under its existing
    mutex. Probe updates copy the affected row and pointer slice; routing reads
    retain the previous snapshot without cloning every row per connection.
    Snapshot ownership and concurrent marshal/update tests cover the race
    detected by real grouped-profile traffic under `-race`.
