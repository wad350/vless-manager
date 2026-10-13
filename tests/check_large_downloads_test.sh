#!/bin/sh
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT

cat >"$scratch/curl" <<'EOF'
#!/bin/sh
case " $* " in *' --max-time '*) exit 99;; esac
case " $* " in *' --speed-time '*' --speed-limit '*) :;; *) exit 98;; esac
case "$MOCK_MODE" in
    complete) printf '200 1024 101.3\n';;
    timeout) printf '200 512 80.0\n'; echo 'operation timed out' >&2; exit 28;;
    truncated) printf '200 512 1.5\n';;
    rejected) printf '503 1024 1.5\n';;
    stuck) exec sleep 30;;
esac
EOF
chmod +x "$scratch/curl"
run() {
    MOCK_MODE=$1 CURL_BIN="$scratch/curl" TMPDIR="$scratch" BYTES=1024 WORKERS=2 \
        BUDGET_SECONDS=${2:-10} sh "$root/tests/check_large_downloads.sh" >"$scratch/output" 2>&1
}
run complete
grep -q 'PASS: 2/2 complete transfers' "$scratch/output"
grep -q 'seconds=101.3' "$scratch/output"
for mode in timeout truncated rejected; do
    code=0
    run "$mode" || code=$?
    [ "$code" -eq 1 ]
    grep -q 'FAIL: 2/2 incomplete transfers' "$scratch/output"
done
code=0
run stuck 1 || code=$?
[ "$code" -eq 2 ]
grep -q 'INCONCLUSIVE' "$scratch/output"
[ "$(find "$scratch" -type d -name 'vless-download.*' | wc -l | tr -d ' ')" -eq 0 ]
echo 'Large-download probe tests passed'
