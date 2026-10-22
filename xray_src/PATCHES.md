# Local Xray patches

Base: [XTLS/Xray-core v26.10.21](https://github.com/XTLS/Xray-core/releases/tag/v26.10.21)
(prerelease), commit `1b1647cd9ddf0abc34b981d4611fc03bcac1ca74`.
Module: `v1.260327.1-0.20261021115930-1b1647cd9ddf`.
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
  Upstream now uses its lightweight stack for optional TUN/WireGuard/MASQUE;
  the default manager still uses TPROXY. Extra UDP stats/ownership tests remain.
- Go 1.27.3 and upstream's updated RootCAs include the revoked-intermediate
  certificate fix. x/crypto 0.58.0 retains the reviewed MIPS crypto overlay.
- Error paths release pooled UDP/read buffers and HTTP response bodies; nil core
  instances/timers, malformed stats names, VLESS padding, routing regexes and REALITY
  escapes return safely. Darwin TCP keepalive options use their correct values.
- SOCKS temporary UDP connections close their sockets only once, including the
  reentrant inactivity-timer callback and concurrent cleanup.
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
