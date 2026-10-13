# Cloudflare Large-Download Diagnosis, 2026-10-13

Router: 192.168.201.1, MT7621, 128 MiB RAM, native TPROXY, manager
1.17.4 and Xray v26.10.10+manager.25. Production timeout settings, crypto
backends and bypass rules were not changed for this investigation.

## Confirmed Cause Of The Large-File Timeout

The earlier auxiliary load generator used `curl --max-time 80` for every
32 MiB file. That is a deadline for the entire request, not a detector of
stalled data. With concurrent speedtest traffic, the available throughput
was insufficient to complete each file before that deadline.

A repeat with the same deadline received 30,177,545 / 30,289,346 /
31,176,422 bytes in three streams. Each still had a nonzero receive rate
when curl cancelled it at exactly 80 seconds. A fourth connection failed
during setup; it must not be classified as a large-file transfer failure.
The old shell generator also ignored individual curl exit codes: `wait`
without arguments did not establish successful completion of each worker.

## Correction And Verification

`tests/check_large_downloads.sh` replaces the fixed per-file deadline with
a low-speed watchdog: less than 1 KiB/s for 15 seconds. Connection setup
retains its 8-second bound. Payloads go to `/dev/null`; only tiny status
files are written to `/tmp`, not NAND.

Every worker must exit successfully, return HTTP 200 and receive exactly
the requested number of bytes. The independent, configurable verification
budget is reported as INCONCLUSIVE (exit 2), not as evidence of a network
stall. Actual transfer failures return exit 1.

Recorded successful transfers through the router's SOCKS/VLESS path:

| Workload | Full Files | Longest Transfer |
| --- | --- | --- |
| One 32 MiB file | 1/1 | 32.32 s |
| Auto profile, four 32 MiB files | 4/4 | 24.43 s |
| Fixed gRPC profile, four 32 MiB files | 4/4 | 38 s |
| Fixed gRPC profile, four 64 MiB files | 4/4 | 180.15 s |
| Fixed gRPC, four 32 MiB files plus manager speedtest | 4/4 | 101.30 s |

The mixed-load manager speedtest completed at 6.30 / 9.37 Mbit/s,
with `tunnel_verified=true`. VPN download counters increased by
157,675,177 bytes, covering the four complete files and speedtest payloads.
Available memory after that test was 35,576 KiB. The zram failed-write
counter did not increase; no OOM or core restart was observed in this run.
The earlier 10-second large-load CPU profile sampled 8.91 CPU seconds;
it did not show GC dominating CPU time.

The verification script has offline tests for successful transfers,
curl timeout exit codes, truncated HTTP-200 bodies, non-200 responses,
budget exhaustion and scratch-file cleanup. They run from `tests/check.sh`.

Run the payload verification on a router with an active tunnel:

```sh
sh tests/check_large_downloads.sh
BYTES=67108864 WORKERS=4 sh tests/check_large_downloads.sh
```

## Separate, Unresolved Connection-Setup Failures

Intermittent TLS-handshake, response-header and worker-preparation timeouts
also occurred before speedtest downloaded any payload, including while
there was no large-download load. These are real failures, distinct from
the confirmed 80-second cancellation above. Both domain-name and pinned
IPv4 curl probes succeeded repeatedly; one pinned-IP probe also timed out.
That does not establish IPv6, DNS, CPU saturation or memory as their cause.

No speculative core patch or increased production timeout was installed.
The successful payload checks do not imply that these separate setup
failures have been fixed, or guarantee stability of every VPN endpoint.

At completion, the original configuration was restored byte-for-byte,
the auto profile was running with 910 bypass domains, diagnostic endpoints
returned HTTP 404, and the installed binary hash remained unchanged.
All temporary router diagnostic files were removed; available memory was
45,316 KiB.
