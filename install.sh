#!/bin/sh
set -eu

REPOSITORY="${VLESS_MANAGER_REPOSITORY:-wad350/vless-manager}"
VERSION="${VLESS_MANAGER_VERSION:-}"
ARCH="mipsel-3.4"
TMP_ROOT="${TMPDIR:-/tmp}"

log() {
    printf '[vless-manager] %s\n' "$*"
}

die() {
    printf '[vless-manager] Ошибка: %s\n' "$*" >&2
    exit 1
}

fetch() {
    url="$1"
    output="$2"

    if command -v curl >/dev/null 2>&1; then
        curl -fL --connect-timeout 15 --retry 2 --retry-delay 2 \
            -A "vless-manager-installer/1" -o "$output" "$url"
        return
    fi
    if command -v wget >/dev/null 2>&1; then
        wget -T 30 -t 3 -U "vless-manager-installer/1" -O "$output" "$url"
        return
    fi
    die "нужен curl или wget"
}

[ "$(id -u)" = "0" ] || die "запустите установку от root"
command -v opkg >/dev/null 2>&1 || die "opkg не найден; сначала установите Entware"
command -v sha256sum >/dev/null 2>&1 || die "sha256sum не найден"

if ! opkg print-architecture 2>/dev/null | awk '{ print $2 }' | grep -qx "$ARCH"; then
    die "пакет предназначен для Entware $ARCH"
fi

tmp_dir="$(mktemp -d "$TMP_ROOT/vless-manager-install.XXXXXX")" ||
    die "не удалось создать временный каталог"
trap 'rm -rf "$tmp_dir"' EXIT HUP INT TERM

if [ -z "$VERSION" ]; then
    log "Проверяю последний стабильный релиз..."
    release_json="$tmp_dir/release.json"
    fetch "https://api.github.com/repos/$REPOSITORY/releases/latest" "$release_json"
    VERSION="$(
        sed -n 's/.*"tag_name":[[:space:]]*"v\([^"]*\)".*/\1/p' \
            "$release_json" | head -n 1
    )"
fi

printf '%s\n' "$VERSION" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$' ||
    die "неверная версия релиза: ${VERSION:-пусто}"

asset="vless-manager_${VERSION}_${ARCH}.ipk"
release_url="https://github.com/$REPOSITORY/releases/download/v${VERSION}"
package_file="$tmp_dir/$asset"
checksum_file="$package_file.sha256"

log "Скачиваю VLESS Manager $VERSION..."
fetch "$release_url/$asset" "$package_file"
fetch "$release_url/$asset.sha256" "$checksum_file"

expected="$(awk 'NR == 1 { print $1 }' "$checksum_file")"
actual="$(sha256sum "$package_file" | awk '{ print $1 }')"
[ -n "$expected" ] && [ "$actual" = "$expected" ] ||
    die "контрольная сумма пакета не совпала"
log "SHA-256 подтверждён."

log "Устанавливаю пакет через opkg..."
opkg install --force-reinstall "$package_file"

installed="$(
    opkg status vless-manager 2>/dev/null |
        awk '$1 == "Version:" { print $2; exit }'
)"
[ "$installed" = "$VERSION" ] ||
    die "opkg завершился, но установлена версия ${installed:-неизвестно}"

log "VLESS Manager $VERSION установлен."
log "WebUI доступен по адресу http://<адрес-роутера>:3001"
