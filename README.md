# VLESS Manager

VLESS Manager is a lightweight VPN manager for Keenetic routers with Entware.
It embeds sing-box, routes router and LAN client traffic through a TUN
interface, manages VLESS subscriptions, selects working servers, monitors
tunnel health, and supports domain bypass rules.

## Build

The Keenetic MT7621 build uses Go 1.24.7 and the bundled sing-box source:

```sh
make ipk
```

The package is written to `build/`.

## Install

Pass router credentials through environment variables. They are intentionally
not stored in the repository:

```sh
make install-ipk PASS='router-password'
```

The default target is `root@192.168.201.1:222`. Override it when necessary:

```sh
make install-ipk \
  ROUTER='root@router-address' \
  PORT='222' \
  PASS='router-password'
```

The web interface listens on port `3001`.

## Source layout

- `cmd/vless-manager/` - manager, API, routing and embedded web interface.
- `singbox_src/` - pinned sing-box source used by the Go module replacement.
- `packaging/` - Entware and OpenWrt package scripts.
