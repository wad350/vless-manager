# Local Xray patches

Base: [XTLS/Xray-core v26.10.10](https://github.com/XTLS/Xray-core/releases/tag/v26.10.10)
(prerelease), commit `701af60772cda123492f86e383e1fdba066614a9`.
Module: `v1.260327.1-0.20261010092107-701af60772cd`.
Keep upstream licenses when updating. This embedded runtime includes local changes.

- Instance-scoped DNS/outbound references and gRPC contexts/client cleanup;
  outbound cache and Observatory snapshots are synchronized across concurrent probes.
- Stats-wrapped readers preserve MultiBuffer and UDP destinations; upload accounting
  includes buffered writes. gRPC mixed reads do not replay consumed response headers.
- gRPC transfers owned payloads and uses size-aware pooled protobuf buffers;
  `XRAY_GRPC_BUFFER_POOL=off` restores the standard decoder. Close cancels RPCs safely.
- Native TPROXY UDP session keys include the original destination. Sessions publish
  initialized cancellation and release queues on expiry/shutdown without removing replacements.
- Linux transparent UDP uses `recvmmsg` batches of up to eight without waiting to fill
  a batch; unsupported kernels use single reads. `XRAY_TPROXY_UDP_BATCH=off` disables it.
  Both paths reject truncation and publish only initialized bytes of receive buffers.
- RAM-only opt-in UDP counters record queue drops, replies and local acceptance;
  they do not prove remote delivery. Pipe reads avoid timers when data is ready;
  inactivity uses wrap-safe 32-bit atomics on MIPS.
- Sniffing deadlines use stopped timers; QUIC rejects unsupported headers before
  allocating handshake scratch buffers.
- Opt-in `randomized-chacha20` TLS fingerprint retains AES fallback and the hybrid
  ML-KEM/X25519 share required by REALITY. Certificate verification is unchanged.
- TUN, WireGuard and MASQUE configuration registration requires `xray_tun`,
  `xray_wireguard`, `xray_masque` tags. Default manager uses Linux TPROXY without gVisor.
  TUN production Go source matches this release; extra ownership/session tests remain.
- Go 1.27.2, x/net 0.60.0 and klauspost/compress 1.18.7 include dependency security fixes.
- Error paths release pooled UDP/read buffers and HTTP response bodies; nil core
  instances/timers, malformed stats names, VLESS padding, routing regexes and REALITY
  escapes return safely. Darwin TCP keepalive options use their correct values.
- Removed unreachable legacy reverse/VMess bodies, private unused state/helpers,
  redundant allocations and one-line package placeholders. Public APIs, registrations,
  generated schemas, optional protocols and active congestion algorithms remain.
- Test fixes synchronize workers and ownership, use temporary certificate/GeoData files,
  and follow optional protocol tags. External ECH tests skip under `-short`.

TCP batching, optional TUN TCP buffer sizes, UDP scheduler yielding and the TUN
payload-copy experiments were removed. Native TPROXY `recvmmsg` remains.
The manager registers upstream `tagged/taggedimpl` for grouped-profile Observatory.

Run `sh tests/check.sh` from the repository root and `go test ./...` in this module.
Optional config tests: `go test -tags 'xray_tun xray_wireguard xray_masque' ./infra/conf`.
Whole-module vet still reports upstream cancellation, protobuf lock-copy and unsafe
pointer diagnostics; a passing test suite does not resolve those diagnostics.
