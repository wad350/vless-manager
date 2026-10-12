# MIPS32r2 Crypto Backends

This is a narrow acceleration of the `golang.org/x/crypto/chacha20` block
routine used by uTLS/Reality. The upstream public API, overlap and counter
checks, buffering and XChaCha key derivation remain unchanged. A separately
gated Poly1305 backend retains the upstream authentication algorithm and state,
and implements complete blocks with saturated 32-bit integer limbs. TLS settings,
certificate verification and routing are unchanged.

`make manager GOARCH=mipsle` stages the pinned x/crypto module in the build
directory and uses an alternate modfile. It never patches GOMODCACHE or the
repository's go.mod. The generator rejects a dependency version/source mismatch
so upstream upgrades require an explicit review. ARM64 builds are unaffected.

The assembly keeps all 16 state words in integer registers and interleaves
independent column/diagonal operations. MIPS32r2 ROTR replaces the compiler's
shift/shift/or sequence; paired LWL/LWR and SWL/SWR handle unaligned buffers.
The assembly processes full 64-byte blocks in batches of at most 16 KiB per
call, with 10 fixed double rounds per block and no data-dependent branches,
table accesses, floating point or allocation. The bounded calls keep the Go
wrapper preemptible between batches. The initial state is immutable and the
upstream wrapper still checks counter exhaustion, overlap and partial blocks.
The single-block implementation remains a test oracle and benchmark baseline.

The output stage now uses ordinary word loads/stores when both public pointers
are word-aligned, and the existing unaligned pairs otherwise. Round arithmetic,
counter handling, batch limits and reserved registers are unchanged. Differential
and protected-page tests cover every source/destination alignment. Three native
16 KiB runs measured 43.7-47.8 MB/s versus 42.7-45.4 MB/s before this change;
the overlapping ranges support only a modest improvement, not a throughput
guarantee. No SIMD, FPU or unavailable AES instruction is assumed.

On the tested MT7621, batching added approximately 3% over the previous assembly
at 16 KiB, not an order-of-magnitude gain. Two independent ChaCha20 workers
achieved approximately 77 MB/s versus 43 MB/s with one; four workers provided
little additional throughput. Full ChaCha20-Poly1305 with two workers measured
approximately 24.6 MB/s versus 11.9 MB/s for Go. These are cipher benchmarks,
not VPN throughput. Parallelism is between independent streams, not goroutines
sharing a cipher state or nonce counter. The runtime default remains two workers.

The assembly backend supports only Linux MIPSLE when every CPU advertises
`mips32r2` in `/proc/cpuinfo`. Unknown CPUs, missing CPU information and MIPSr6
fall back to the unmodified upstream Go implementation. The service init script
defaults to CPU-gated `auto` only for the tested MT7621 topology (two cores,
four VPEs). Other MIPS systems retain `VLESS_MANAGER_MIPS_CHACHA20=generic`.
Native TPROXY tests included RFC/AEAD/concurrency checks, HTTPS, DoH, UDP DNS,
and the Mac Hysteria2-over-VLESS chain. Download CPU cost measured about
51.3-53.8 process ticks/MiB versus 64.8-71.0 for Go in successful transfers;
these are workload measurements, not a guaranteed Internet speed increase.
Intermittent TLS timeouts occurred in both modes and remain an unresolved
network-test limitation. `generic` explicitly forces the upstream Go fallback.
Direct binary invocations without this environment variable
use CPU-gated assembly. A completely unpatched
binary can also be built with `make manager MIPS_CRYPTO=0 GOARCH=mipsle`.

`VLESS_MANAGER_MIPS_POLY1305=auto` enables the Poly1305 backend on the same
supported CPU. The init script now selects it on the tested MT7621 topology;
direct invocations without this variable keep upstream Poly1305. Setting it to
`generic`, or forcing ChaCha20 to `generic`, disables it. Initialization,
buffering, partial final blocks, finalization and tag verification stay upstream.

Complete blocks use twenty unsigned 32-bit hardware products per message block.
The accumulator and clamped key remain in registers throughout a bounded call
of at most 16 KiB. Carry propagation and the partial reduction `c*4 + c` match
the pinned upstream algorithm exactly, including its noncanonical accumulator.
All branches depend only on the public block count; LWL/LWR loads handle all
input alignments. R22/R23/R30 and kernel scratch registers R26/R27 are forbidden.
Calls allocate no heap buffers and remain preemptible between bounded batches.

On 2026-10-12, native MT7621 tests at 192.168.201.1 passed three repetitions of
the complete Poly1305 and AEAD suites, protected-page tests, exact-state random
comparisons, maximal carries and forced-GC concurrency. Three alternating runs
with ChaCha20 held in `auto` measured these median AEAD rates with two workers:

| Bytes | Operation | Upstream Poly1305 | Block assembly | Change |
| ---: | --- | ---: | ---: | ---: |
| 64 | Seal | 8.40 MB/s | 11.91 MB/s | +42% |
| 64 | Open | 9.53 MB/s | 11.80 MB/s | +24% |
| 1200 | Seal | 22.84 MB/s | 34.20 MB/s | +50% |
| 1200 | Open | 22.48 MB/s | 34.14 MB/s | +52% |
| 16384 | Seal | 25.30 MB/s | 38.89 MB/s | +54% |
| 16384 | Open | 25.14 MB/s | 38.98 MB/s | +55% |

Raw single-worker Poly1305 at 16 KiB measured 40.67 MB/s versus 19.63 MB/s
for Go. The previous six-product assembly prototype remains a test baseline,
not the production path; it measured 20.39 MB/s. The fresh CPU profiles confirm
the block assembly path replaced Go multiplication, carries and reduction.
These are native cipher benchmarks, not promised modem or VPN throughput.
The retained Go implementation remains an independent arithmetic oracle.
Neither these tests nor branch-free assembly substitute for a cryptographic audit.

Regenerate and stage from the repository root:

```sh
go run ./packaging/mipscrypto -generate
go test ./packaging/mipscrypto
go run ./packaging/mipscrypto -out build/mips-crypto
GOOS=linux GOARCH=mipsle GOMIPS=softfloat CGO_ENABLED=0 \
  go test -modfile=build/mips-crypto/crypto.mod -c \
  -o build/chacha.test golang.org/x/crypto/chacha20
GOOS=linux GOARCH=mipsle GOMIPS=softfloat CGO_ENABLED=0 \
  go test -modfile=build/mips-crypto/crypto.mod -c \
  -o build/aead.test golang.org/x/crypto/chacha20poly1305
GOOS=linux GOARCH=mipsle GOMIPS=softfloat CGO_ENABLED=0 \
  go test -modfile=build/mips-crypto/crypto.mod -c \
  -o build/poly.test golang.org/x/crypto/internal/poly1305
```

Run the small test binaries on a compatible router from RAM, not NAND:

```sh
GOMAXPROCS=1 GOMEMLIMIT=16MiB /tmp/chacha.test -test.v -test.count=3
VLESS_MANAGER_MIPS_CHACHA20=generic GOMAXPROCS=1 GOMEMLIMIT=16MiB \
  /tmp/chacha.test -test.v
GOMAXPROCS=1 GOMEMLIMIT=16MiB /tmp/aead.test -test.v -test.count=3
VLESS_MANAGER_MIPS_CHACHA20=auto VLESS_MANAGER_MIPS_POLY1305=auto \
  GOMAXPROCS=2 GOMEMLIMIT=16MiB /tmp/aead.test -test.v -test.count=3
VLESS_MANAGER_MIPS_CHACHA20=auto VLESS_MANAGER_MIPS_POLY1305=auto \
  GOMAXPROCS=2 GOMEMLIMIT=16MiB /tmp/poly.test -test.v -test.count=3
GOMAXPROCS=1 GOMEMLIMIT=16MiB /tmp/chacha.test -test.run='^$' \
  -test.bench=BenchmarkMIPSChaCha -test.benchtime=300ms -test.count=3
```

Tests include upstream RFC vectors, unaligned buffers, partial/overlapping
buffers, counter exhaustion, XChaCha and authentication failures, plus
deterministic assembly-vs-generic comparisons for random keys/nonces/counters.
Protected-page boundary and eight-worker tests with forced GC cover invalid
memory accesses and register/context-switch preservation on the real CPU.
Poly1305 tests compare carries, 100,000 random general products, fused clamped
products and complete MAC states against the retained upstream implementation,
including 16 KiB batching boundaries, protected pages and maximal carries.
AEAD tests include eight independent workers, forced GC and rejected modified
authentication tags. Native tests are necessary; host tests alone do not execute
MIPS instructions. ARM64 and pure-Go builds also exercise the portable fallback.
Benchmark throughput measures the cipher routine, not the whole VPN. Network
throughput also depends on modem, remote server, TCP/gRPC and routing.

To reproduce the comparison, alternate `VLESS_MANAGER_MIPS_POLY1305=generic`
and `auto` while keeping ChaCha20 and other environment settings unchanged:

```sh
VLESS_MANAGER_MIPS_CHACHA20=auto VLESS_MANAGER_MIPS_POLY1305=generic \
  GOMAXPROCS=2 GOMEMLIMIT=16MiB /tmp/aead.test -test.run='^$' \
  -test.bench=BenchmarkMIPSAEADSizes -test.benchtime=500ms -test.count=3
VLESS_MANAGER_MIPS_CHACHA20=auto VLESS_MANAGER_MIPS_POLY1305=auto \
  GOMAXPROCS=2 GOMEMLIMIT=16MiB /tmp/aead.test -test.run='^$' \
  -test.bench=BenchmarkMIPSAEADSizes -test.benchtime=500ms -test.count=3
GOMAXPROCS=1 GOMEMLIMIT=16MiB /tmp/poly.test -test.run='^$' \
  -test.bench=BenchmarkMIPSPoly1305 -test.benchtime=500ms -test.count=3
```

The aligned output stage schedules independent state/source word loads before
their arithmetic uses, without adding instructions or changing the rounds.
Five alternating MT7621 measurements of 16 KiB blocks gave median throughput
47.77 MB/s before and 48.89 MB/s after this scheduling change (about 2.3%).
The small gain is a cipher microbenchmark, not an Internet-speed guarantee.
Three native repetitions of the complete ChaCha20 and AEAD suites passed.

Instruction and ABI references:
- [Go assembler, MIPS](https://go.dev/doc/asm#MIPS)
- [MIPS32 instruction manual](https://s3-eu-west-1.amazonaws.com/downloads-mips/documents/MD00086-2B-MIPS32BIS-AFP-05.04.pdf)
