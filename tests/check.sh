#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$ROOT"

dependencies=$(go list -deps ./cmd/vless-manager)
case "$dependencies" in
    *gvisor.dev/gvisor/*)
        echo "Default manager build must not link gVisor" >&2
        exit 1
        ;;
esac

go test -race -short -count=1 -timeout=120s ./cmd/vless-manager ./packaging/mipscrypto
(
    cd xray_src
    go test -count=1 -run '^TestReadAllToBytesReleasesBuffersOnError$' ./common/buf
    go test -race -count=1 -run 'TestCreateObjectInstanceContext|TestFromContextPanic' ./core
    go test -race -short -count=1 -timeout=120s \
        ./common/buf ./common/crypto/... ./common/protocol/quic ./common/protocol/udp ./common/task \
        ./transport/pipe ./transport/internet ./transport/internet/grpc/... \
        ./transport/internet/tls ./transport/internet/udp \
        ./app/observatory ./app/observatory/burst ./app/stats/command ./app/proxyman/inbound ./app/proxyman/outbound \
        ./proxy/blackhole ./proxy/dokodemo ./proxy/socks ./proxy/vless/encoding ./proxy/vless/outbound \
        ./proxy/vmess/encoding ./infra/conf ./transport/internet/finalmask
)
node --check cmd/vless-manager/web/app.js
node --test tests/traffic_chart_test.mjs
sh -n install.sh
sh -n packaging/build_ipk.sh
sh -n packaging/init.d/S99vless-manager
sh tests/install_test.sh
sh tests/check_large_downloads_test.sh
