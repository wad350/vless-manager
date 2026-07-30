#!/bin/sh
set -eu

ROOT="$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)"
TEST_DIR="$(mktemp -d "${TMPDIR:-/tmp}/vless-manager-installer-test.XXXXXX")"
trap 'rm -rf "$TEST_DIR"' EXIT HUP INT TERM

mkdir -p "$TEST_DIR/bin" "$TEST_DIR/fixtures"
printf 'test ipk payload\n' >"$TEST_DIR/fixtures/package.ipk"
TEST_SHA="$(sha256sum "$TEST_DIR/fixtures/package.ipk" | awk '{ print $1 }')"
export TEST_DIR TEST_SHA

cat >"$TEST_DIR/bin/id" <<'EOF'
#!/bin/sh
[ "${1:-}" = "-u" ] && { printf '0\n'; exit 0; }
exec /usr/bin/id "$@"
EOF

cat >"$TEST_DIR/bin/curl" <<'EOF'
#!/bin/sh
output=
url=
while [ "$#" -gt 0 ]; do
    case "$1" in
        -o)
            output="$2"
            shift 2
            ;;
        *)
            url="$1"
            shift
            ;;
    esac
done
case "$url" in
    */releases/latest)
        printf '{"tag_name":"v9.8.7"}\n' >"$output"
        ;;
    *.ipk.sha256)
        printf '%s  vless-manager_9.8.7_mipsel-3.4.ipk\n' "$TEST_SHA" >"$output"
        ;;
    *.ipk)
        cp "$TEST_DIR/fixtures/package.ipk" "$output"
        ;;
    *)
        printf 'unexpected URL: %s\n' "$url" >&2
        exit 1
        ;;
esac
EOF

cat >"$TEST_DIR/bin/opkg" <<'EOF'
#!/bin/sh
case "$1" in
    print-architecture)
        printf 'arch all 1\narch mipsel-3.4 10\n'
        ;;
    install)
        package=
        for arg in "$@"; do package="$arg"; done
        version="$(basename "$package" | sed -n 's/^vless-manager_\([^_]*\)_.*/\1/p')"
        printf '%s\n' "$version" >"$TEST_DIR/installed-version"
        ;;
    status)
        printf 'Package: vless-manager\nVersion: %s\n' \
            "$(cat "$TEST_DIR/installed-version")"
        ;;
    *)
        exit 1
        ;;
esac
EOF

chmod +x "$TEST_DIR/bin/id" "$TEST_DIR/bin/curl" "$TEST_DIR/bin/opkg"

PATH="$TEST_DIR/bin:$PATH" TMPDIR="$TEST_DIR" \
    sh "$ROOT/install.sh" >"$TEST_DIR/output.log"

grep -q 'VLESS Manager 9.8.7 установлен' "$TEST_DIR/output.log"
grep -qx '9.8.7' "$TEST_DIR/installed-version"
printf 'install.sh integration test passed\n'
