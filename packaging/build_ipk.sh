#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT="$SCRIPT_DIR/.."
BUILD="$ROOT/build"
VERSION="${VERSION:-1.2.0}"
ARCH="${ARCH:-mipsel-3.4}"
PKG_NAME="vless-manager_${VERSION}_${ARCH}"
PKG_DIR="$BUILD/${PKG_NAME}"

echo "Building IPK: $PKG_NAME"

rm -rf "$PKG_DIR"
mkdir -p "$PKG_DIR/CONTROL"
mkdir -p "$PKG_DIR/opt/bin"
mkdir -p "$PKG_DIR/opt/etc/vless-manager"
mkdir -p "$PKG_DIR/opt/etc/init.d"
mkdir -p "$PKG_DIR/opt/var/run"
mkdir -p "$PKG_DIR/opt/share/vless-manager"

# Binary: vless-manager embeds sing-box as a Go library.
cp "$BUILD/vless-manager" "$PKG_DIR/opt/bin/vless-manager"
chmod 755 "$PKG_DIR/opt/bin/vless-manager"

# Init script
cp "$SCRIPT_DIR/init.d/S99vless-manager" "$PKG_DIR/opt/etc/init.d/"
chmod 755 "$PKG_DIR/opt/etc/init.d/S99vless-manager"

# Vendored iptables IPK so postinst can install it offline when Entware
# repo is unreachable (typical for LTE setups with DPI). The router won't
# have any routing applied without iptables — this is a hard runtime dep.
if [ -f "$SCRIPT_DIR/vendor/iptables_kn.ipk" ]; then
    cp "$SCRIPT_DIR/vendor/iptables_kn.ipk" "$PKG_DIR/opt/share/vless-manager/iptables_kn.ipk"
fi

# CONTROL
cat > "$PKG_DIR/CONTROL/control" <<EOF
Package: vless-manager
Version: ${VERSION}
Architecture: ${ARCH}
Maintainer: local
Description: VLESS proxy manager with WebUI for Keenetic routers. Embeds sing-box
 as a library and runs a TUN/system-stack transparent tunnel. Policy routing
 sends LAN and router TCP/UDP traffic through VLESS while LAN/private traffic,
 DNS, QUIC fallback drops, and manager health probes stay on the WAN route.
 iptables is shipped bundled (postinst unpacks /opt/share/vless-manager/iptables_kn.ipk
 if iptables is missing) so the package installs on a fresh router without
 reaching the Entware repo.
EOF

cat > "$PKG_DIR/CONTROL/postinst" <<'EOF'
#!/bin/sh
[ -n "$IPKG_INSTROOT" ] && exit 0

# Hard runtime dependency: routing.go calls iptables for the mangle/MARK rules
# that send LAN traffic through tun0. Keenetic ships without it and Entware
# repo is often unreachable from LTE (DPI), so we ship a copy and unpack it
# in-place via plain tar — no opkg metadata round-trip required.
VENDOR=/opt/share/vless-manager/iptables_kn.ipk
if ! command -v iptables >/dev/null 2>&1 && [ -f "$VENDOR" ]; then
    echo "[postinst] iptables missing, unpacking bundled $VENDOR"
    TMP=$(mktemp -d /tmp/iptables-install.XXXXXX)
    if cd "$TMP" && tar -xzf "$VENDOR" && tar -xzf data.tar.gz; then
        # xtables-multi is the real binary; iptables/iptables-* are symlinks.
        if [ -f opt/sbin/xtables-multi ]; then
            cp -a opt/sbin/xtables-multi /opt/sbin/
            cp -a opt/lib/. /opt/lib/ 2>/dev/null
            for n in iptables iptables-save iptables-restore ip6tables ip6tables-save ip6tables-restore; do
                ln -sf xtables-multi /opt/sbin/$n
            done
            echo "[postinst] iptables installed: $(/opt/sbin/iptables -V 2>&1)"
        fi
    fi
    rm -rf "$TMP"
fi

/opt/etc/init.d/S99vless-manager start
exit 0
EOF
chmod 755 "$PKG_DIR/CONTROL/postinst"

cat > "$PKG_DIR/CONTROL/prerm" <<'EOF'
#!/bin/sh
[ -n "$IPKG_INSTROOT" ] && exit 0
/opt/etc/init.d/S99vless-manager stop 2>/dev/null
exit 0
EOF
chmod 755 "$PKG_DIR/CONTROL/prerm"

SIZE=$(du -sk "$PKG_DIR/opt" | cut -f1)
echo "Installed-Size: $SIZE" >> "$PKG_DIR/CONTROL/control"

cd "$BUILD"
IPK="${PKG_NAME}.ipk"
rm -f "$IPK"
export PKG_NAME
export IPK
python3 <<'PY'
import os
import gzip
import tarfile
from pathlib import Path
build = Path('.').resolve()
pkg = build / os.environ['PKG_NAME']
ctrl_dir = pkg / 'CONTROL'

# build control.tar.gz
with tarfile.open(pkg / 'control.tar.gz', 'w:gz', format=tarfile.USTAR_FORMAT) as tar:
    dirinfo = tarfile.TarInfo('.')
    dirinfo.type = tarfile.DIRTYPE
    dirinfo.mode = 0o755
    dirinfo.uid = 0
    dirinfo.gid = 0
    dirinfo.uname = 'root'
    dirinfo.gname = 'root'
    tar.addfile(dirinfo)
    for root, dirs, files in os.walk(ctrl_dir):
        root_path = Path(root)
        for name in dirs + files:
            path = root_path / name
            arcname = './' + str(path.relative_to(ctrl_dir))
            tarinfo = tar.gettarinfo(str(path), arcname=str(arcname))
            tarinfo.uid = 0
            tarinfo.gid = 0
            tarinfo.uname = 'root'
            tarinfo.gname = 'root'
            if tarinfo.isreg():
                with open(path, 'rb') as f:
                    tar.addfile(tarinfo, f)
            else:
                tar.addfile(tarinfo)

# build data.tar.gz
with tarfile.open(pkg / 'data.tar.gz', 'w:gz', format=tarfile.USTAR_FORMAT) as tar:
    opt_root = pkg / 'opt'
    for root, dirs, files in os.walk(opt_root):
        root_path = Path(root)
        for name in dirs + files:
            path = root_path / name
            arcname = path.relative_to(pkg)
            tarinfo = tar.gettarinfo(str(path), arcname=str(arcname))
            if tarinfo.isreg():
                with open(path, 'rb') as f:
                    tar.addfile(tarinfo, f)
            else:
                tar.addfile(tarinfo)

# write debian-binary
with open(pkg / 'debian-binary', 'wb') as f:
    f.write(b'2.0\n')

# write top-level IPK tarball wrapper
files = ['debian-binary', 'control.tar.gz', 'data.tar.gz']
with tarfile.open(build / os.environ['IPK'], 'w:gz', format=tarfile.USTAR_FORMAT) as out_tar:
    for name in files:
        path = pkg / name
        tarinfo = out_tar.gettarinfo(str(path), arcname=name)
        if tarinfo.isreg():
            tarinfo.uid = 0
            tarinfo.gid = 0
            tarinfo.uname = 'root'
            tarinfo.gname = 'root'
            with open(path, 'rb') as f:
                out_tar.addfile(tarinfo, f)
        else:
            out_tar.addfile(tarinfo)
PY

echo "Built: $BUILD/$IPK ($(du -h "$BUILD/$IPK" | cut -f1))"
echo "Installed size: ~${SIZE}KB"
