#!/bin/sh
# Run on the router: payloads are discarded and scratch files stay in RAM.
set -u

BYTES=${BYTES:-33554432}
WORKERS=${WORKERS:-4}
SOCKS=${SOCKS:-127.0.0.1:7891}
CONNECT_SECONDS=${CONNECT_SECONDS:-8}
STALL_SECONDS=${STALL_SECONDS:-15}
MIN_BYTES_PER_SECOND=${MIN_BYTES_PER_SECOND:-1024}
BUDGET_SECONDS=${BUDGET_SECONDS:-300}
CURL_BIN=${CURL_BIN:-curl}

for value in "$BYTES" "$WORKERS" "$CONNECT_SECONDS" "$STALL_SECONDS" "$MIN_BYTES_PER_SECOND" "$BUDGET_SECONDS"; do
    case "$value" in ''|*[!0-9]*) echo 'Invalid numeric setting' >&2; exit 1;; esac
    if [ "$value" -lt 1 ] || [ "$value" -gt 1073741824 ]; then
        echo 'Numeric setting out of range' >&2
        exit 1
    fi
done
if [ "$WORKERS" -gt 4 ] || [ "$BYTES" -gt 67108864 ]; then
    echo 'Maximum: four workers, 64 MiB per worker' >&2
    exit 1
fi

scratch=$(mktemp -d "${TMPDIR:-/tmp}/vless-download.XXXXXX") || exit 1
pids=''
cleanup() {
    for pid in $pids; do kill "$pid" 2>/dev/null || :; done
    for pid in $pids; do wait "$pid" 2>/dev/null || :; done
    rm -rf "$scratch"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM HUP

worker() {
    trap - EXIT
    index=$1
    curl_pid=''
    trap 'if [ -n "$curl_pid" ]; then kill "$curl_pid" 2>/dev/null || :; wait "$curl_pid" 2>/dev/null || :; fi; exit 143' TERM INT HUP
    # An absolute per-file deadline confuses a slow transfer with a stall.
    "$CURL_BIN" --socks5-hostname "$SOCKS" --connect-timeout "$CONNECT_SECONDS" \
        --speed-time "$STALL_SECONDS" --speed-limit "$MIN_BYTES_PER_SECOND" \
        --silent --show-error --output /dev/null \
        --write-out '%{http_code} %{size_download} %{time_total}\n' \
        "https://speed.cloudflare.com/__down?bytes=$BYTES" \
        >"$scratch/$index.result" 2>"$scratch/$index.error" &
    curl_pid=$!
    wait "$curl_pid"
    code=$?
    printf '%s\n' "$code" >"$scratch/$index.exit"
}

index=1
while [ "$index" -le "$WORKERS" ]; do
    worker "$index" &
    pids="$pids $!"
    index=$((index + 1))
done

started=$(date +%s)
budget_exhausted=0
while :; do
    finished=0
    index=1
    while [ "$index" -le "$WORKERS" ]; do
        [ ! -f "$scratch/$index.exit" ] || finished=$((finished + 1))
        index=$((index + 1))
    done
    [ "$finished" -lt "$WORKERS" ] || break
    if [ "$(( $(date +%s) - started ))" -ge "$BUDGET_SECONDS" ]; then
        budget_exhausted=1
        for pid in $pids; do kill "$pid" 2>/dev/null || :; done
        break
    fi
    sleep 1
done
for pid in $pids; do wait "$pid" 2>/dev/null || :; done
pids=''

failed=0
index=1
while [ "$index" -le "$WORKERS" ]; do
    http=000; bytes=0; elapsed=0; code=143
    if [ -s "$scratch/$index.result" ]; then
        read -r http bytes elapsed <"$scratch/$index.result" || :
    fi
    if [ -f "$scratch/$index.exit" ]; then read -r code <"$scratch/$index.exit"; fi
    printf 'worker=%s curl_exit=%s http=%s bytes=%s expected=%s seconds=%s\n' \
        "$index" "$code" "$http" "$bytes" "$BYTES" "$elapsed"
    if [ "$code" != 0 ] || [ "$http" != 200 ] || [ "$bytes" != "$BYTES" ]; then
        failed=$((failed + 1))
        head -c 1024 "$scratch/$index.error" >&2
    fi
    index=$((index + 1))
done
if [ "$budget_exhausted" -eq 1 ]; then
    echo 'INCONCLUSIVE: verification budget exhausted; not evidence of a network stall' >&2
    exit 2
fi
if [ "$failed" -gt 0 ]; then
    printf 'FAIL: %s/%s incomplete transfers\n' "$failed" "$WORKERS" >&2
    exit 1
fi
printf 'PASS: %s/%s complete transfers\n' "$WORKERS" "$WORKERS"
