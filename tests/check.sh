#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$ROOT"

go test -race -short -count=1 -timeout=120s ./cmd/vless-manager ./packaging/mipscrypto
(
    cd xray_src
    go test -race -short -count=1 -timeout=120s \
        ./common/buf ./common/protocol/quic ./common/protocol/udp ./common/task \
        ./transport/pipe ./transport/internet ./transport/internet/grpc/... \
        ./transport/internet/tls ./transport/internet/udp \
        ./app/observatory ./app/proxyman/inbound ./app/proxyman/outbound \
        ./proxy/blackhole ./proxy/dokodemo ./proxy/vless/outbound
)
node --check cmd/vless-manager/web/app.js
sh -n install.sh
sh -n packaging/build_ipk.sh
sh -n packaging/init.d/S99vless-manager
sh tests/install_test.sh
