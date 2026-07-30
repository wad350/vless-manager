#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT="$SCRIPT_DIR/../.."
BUILD="$ROOT/build"
VERSION="${VERSION:-1.3.0}"
ARCH="mipsel_24kc"
PKG_NAME="vless-manager_${VERSION}_${ARCH}"
PKG_DIR="$BUILD/${PKG_NAME}"

echo "Building OpenWrt IPK: $PKG_NAME"

rm -rf "$PKG_DIR"
mkdir -p "$PKG_DIR/CONTROL"
mkdir -p "$PKG_DIR/usr/bin"
mkdir -p "$PKG_DIR/etc/vless-manager"
mkdir -p "$PKG_DIR/etc/init.d"

# Manager binary only — sing-box comes from the OpenWrt repo (Depends below).
cp "$BUILD/vless-manager" "$PKG_DIR/usr/bin/vless-manager"
chmod 755 "$PKG_DIR/usr/bin/vless-manager"

# procd init script
cp "$SCRIPT_DIR/init.d/vless-manager" "$PKG_DIR/etc/init.d/vless-manager"
chmod 755 "$PKG_DIR/etc/init.d/vless-manager"

# CONTROL
cat > "$PKG_DIR/CONTROL/control" <<EOF
Package: vless-manager
Version: ${VERSION}
Architecture: ${ARCH}
Maintainer: local
Section: net
Depends: sing-box, iptables-zz-legacy, iptables-mod-tproxy, iptables-mod-socket, kmod-ipt-tproxy, kmod-ipt-socket, ip-full
Description: Whitelist-driven VLESS auto-failover manager for OpenWrt.
 Web UI + subscriptions + ping tests. Wraps sing-box configured as a TPROXY
 transparent proxy (mangle + DIVERT via xt_socket) — kernel does L3/L4,
 sing-box just forwards bytes. Toggles VPN on/off based on whitelist
 detection (mobile-operator zero-rated probes vs open-internet probes).
EOF

SIZE=$(du -sk "$PKG_DIR/usr" "$PKG_DIR/etc" | tail -1 | cut -f1)
echo "Installed-Size: $SIZE" >> "$PKG_DIR/CONTROL/control"

# postinst — enable + start the service on install
cat > "$PKG_DIR/CONTROL/postinst" <<'EOF'
#!/bin/sh
[ -n "$IPKG_INSTROOT" ] && exit 0
/etc/init.d/vless-manager enable
/etc/init.d/vless-manager start
exit 0
EOF
chmod 755 "$PKG_DIR/CONTROL/postinst"

# prerm — stop + disable
cat > "$PKG_DIR/CONTROL/prerm" <<'EOF'
#!/bin/sh
[ -n "$IPKG_INSTROOT" ] && exit 0
/etc/init.d/vless-manager stop 2>/dev/null
/etc/init.d/vless-manager disable 2>/dev/null
exit 0
EOF
chmod 755 "$PKG_DIR/CONTROL/prerm"

cd "$BUILD"
IPK="${PKG_NAME}.ipk"
rm -f "$IPK"
COPYFILE_DISABLE=1 tar -czf "${PKG_NAME}/data.tar.gz" --no-xattrs -C "${PKG_NAME}" usr etc
COPYFILE_DISABLE=1 tar -czf "${PKG_NAME}/control.tar.gz" --no-xattrs -C "${PKG_NAME}/CONTROL" .
echo "2.0" > "${PKG_NAME}/debian-binary"
ar -qc "$IPK" "${PKG_NAME}/debian-binary" "${PKG_NAME}/control.tar.gz" "${PKG_NAME}/data.tar.gz"

echo "Built: $BUILD/$IPK ($(du -h "$BUILD/$IPK" | cut -f1))"
echo "Installed size: ~${SIZE}KB"
