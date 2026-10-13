# Speedtest Connection Recovery, 2026-10-13

## Diagnosis

Router: 192.168.201.1, MT7621, 128 MiB RAM, native TPROXY, Xray
v26.10.10+manager.25. A fixed gRPC/Reality profile was used to avoid confusing
server selection with transport changes.

The unmodified Go TLS client sometimes wrote its ClientHello and received
zero response bytes for the entire existing eight-second handshake limit.
This occurred both through the manager and through an independent SOCKS
client. It was not a certificate-validation error or a bad-record-MAC error.

Experiments did not establish an ML-KEM incompatibility:

- The first idle series gave 2 failures in 32 default handshakes and none in
  32 smaller, classical-only handshakes.
- Interleaving both variants under load then gave 2 failures in 32 default
  connections and 3 in 32 smaller connections. The latter included HTTP
  response-header failures after a successful handshake.
- Another 80-connection series gave 2 failures in 40 requests using a domain
  name and 1 in 40 using a pinned IPv4 address. Pinning DNS was not a fix.
- A debug series produced 4 failures in 48 independent connections. During
  the stalls, Xray goroutines awaited initial gRPC response headers while
  the write loop was idle and other streams were functioning.

The evidence localizes the missing response to individual tunnel streams;
it does not prove whether the remote proxy, its egress network, or the
destination caused it. Server-side logs would be needed to distinguish
those possibilities. It does not justify weakening TLS, disabling ML-KEM,
changing the modem, or attributing the failures to MIPS assembly.

The application-level defect was treating a single unanswered setup stream
as a fatal speedtest failure. An additional Speedtest.ru failure occurred
after receiving 26 MB: an unanswered GET for the next block terminated the
sample even though the tunnel was still usable.

## Changes

- Tunnel speedtests may open one spare TLS handshake after 1.5 seconds.
  Both attempts use the same target, certificate policy, and original
  eight-second deadline. The losing raw tunnel connection is closed;
  no HTTP request is sent on it. A fast connection creates no spare.
- Connection preparation has at most three attempts for either provider,
  only before any measured payload. Cancellation, stopped VPN, certificate
  errors, and failures after payload prevent restarting the sample.
- An unanswered download GET may be repeated once with the same phase
  context and counter. The wait remains in measured elapsed time. Partially
  read response bodies are not replayed or treated as success.
- TLS and response-header timeout settings remain eight seconds. No core,
  routing, DNS, crypto, or modem configuration was changed.
- Temporary settings, packet capture, and diagnostic logs used router RAM.
  No persistent diagnostic settings were installed; deployment replaced the
  package executable, then restored the normal service.

## Regression Coverage

Tests cover a deliberately silent first TLS connection, spare cancellation,
fast connection reuse, certificate rejection without retry, a shared overall
deadline, cancellation of both handshakes, standard HTTP TLS trace delivery,
bounded preparation attempts, no sample restart after payload, download GET
recovery, persistent failure, and retention of partial-body errors.

The full `tests/check.sh` suite, including the race detector and patched
Xray packages, and `go vet ./cmd/vless-manager` passed.

The first candidate was not accepted as fully verified: Cloudflare completed
3 of 4 tests, while both Speedtest.ru attempts failed during download. This
led to the bounded setup and unanswered-GET recovery changes above.

## Final Installed Build

Manager 1.17.5 was installed on 192.168.201.1. The installed binary's SHA-256
matches the locally built package. Xray remains v26.10.10+manager.25.

Six consecutive tests of the final build completed with `tunnel_verified=true`:

| Provider | Background Load | Download, Mbit/s | Upload, Mbit/s | Elapsed, s |
| --- | --- | ---: | ---: | ---: |
| Cloudflare | None | 8.83 | 14.53 | 55.74 |
| Speedtest.ru, Rostelecom | None | 7.40 | 21.23 | 55.85 |
| Cloudflare | Four concurrent downloads | 4.43 | 17.42 | 55.18 |
| Speedtest.ru, CaspiyTelecom | Four concurrent downloads | 3.32 | 15.14 | 76.01 |
| Cloudflare | Four concurrent downloads | 4.13 | 10.71 | 65.26 |
| Cloudflare | Four concurrent downloads | 4.25 | 13.61 | 62.20 |

Each background worker requested a 64 MiB object and made progress during
the speedtest. Workers were stopped after the speedtest; these are not
claims that every background object finished. The bandwidth test uses its
sampling window and actual transferred bytes, not completion of a fixed
100 MB download.

The spare TLS connection path activated during both Speedtest.ru runs.
No whole-test preparation retry was needed in this six-test series.
After-test samples showed 20-25 goroutines, 2.8-4.5 MiB of allocated Go heap,
and 31.8-33.4 MiB of system available memory. These are after-test samples,
not peak measurements. The historical zram failed-write counter remained
326 throughout the series. Cancellation reached `cancelled`; runtime
goroutine counts were 23 before and 23 after cancellation.

The normal service and original auto-selection profile were then restored.
User settings and both automation flags were preserved. Diagnostics returned
HTTP 404, and temporary router capture/probe files and processes were removed.
The bypass list remains 910 domains.

Six routing checks passed in the normal service, using WAN packet captures
and VPN/bypass byte counters rather than HTTP success alone:

- Public Cloudflare HTTPS and non-bypass Mail.ru HTTPS used the VPN, with
  no clear destination request on the modem WAN.
- Yandex `ya.ru` HTTP and HTTPS used direct WAN, with visible Host/SNI and
  positive bypass counters.
- A UDP DNS query to Quad9 returned an answer through the VPN, without a
  clear UDP/53 request to Quad9 on WAN.
- The local router HTTP panel remained accessible without VPN traffic.

A seventh speedtest in the restored normal service also completed through
the VPN: Cloudflare, 21.43 Mbit/s download and 12.82 Mbit/s upload, 71,303,168
download bytes and 40,894,464 upload bytes, in 59.25 seconds. Afterwards,
system available memory was 39.9 MiB, and the VPN and routing remained active.

Several minutes later, the original auto-selection profile failed five
consecutive tunnel health checks. The unchanged automatic controller stopped
it, tested the subscription, and selected the previously tested working gRPC
profile. VPN health recovered without changing the automation settings. This
later transport failure must not be mistaken for a failed seventh speedtest.

An eighth test then completed in the normal service on that gRPC profile,
with tunnel health monitoring enabled: Speedtest.ru, Rostelecom Vladikavkaz,
28.46 Mbit/s download and 12.61 Mbit/s upload. It transferred 89,000,000
download bytes and 41,266,432 upload bytes in 77.55 seconds, with
`tunnel_verified=true`. Twelve seconds after completion, VPN health and
routing were still ready; system available memory was 41.5 MiB. The manager
is left running normally on this automatically selected gRPC profile, not
forced back to the unstable original profile.

These results verify bounded recovery in the tested conditions. They do not
prove that the remote source of unanswered gRPC streams has disappeared, nor
that an arbitrarily failing tunnel can always complete a speedtest.
