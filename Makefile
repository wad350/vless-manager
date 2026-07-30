# ── Project metadata ──────────────────────────────────────────────────────────
VERSION      := 1.15.2
ARCH         := mipsel-3.4
BUILD_DATE   := $(shell date -u +%Y-%m-%d)

# ── Embedded sing-box ─────────────────────────────────────────────────────────
# Official sing-box is embedded as a Go library (singbox_src/).
# No separate binary — saves ~15 MB RSS on the 124 MB MT7621 router.
SINGBOX_TAG  := v1.13.14
BUILD_TAGS   := with_utls
UPDATE_REPOSITORY ?= wad350/vless-manager

# ── Go cross-compile target (Keenetic MT7621 = mipsle softfloat) ──────────────
GOOS         := linux
GOARCH       := mipsle
GOMIPS       := softfloat
CGO          := 0
# Pin the toolchain used by the official sing-box source.
# Go 1.25+ on softfloat MIPS reserves >500 MB virtual memory even at idle
# and triggered OOM-killer on 124 MB routers in earlier tests.
GOTOOLCHAIN  := go1.24.7
export GOTOOLCHAIN
LDFLAGS      := -s -w \
                -X main.Version=$(VERSION) \
                -X main.BuildDate=$(BUILD_DATE) \
                -X main.BundledSingBox=$(SINGBOX_TAG) \
                -X main.UpdateRepository=$(UPDATE_REPOSITORY)

# ── Paths ─────────────────────────────────────────────────────────────────────
BUILD_DIR    := build

# ── Router deploy (direct SCP) ────────────────────────────────────────────────
ROUTER       ?= root@192.168.201.1
PORT         ?= 222
PASS         ?=

.PHONY: all manager ipk clean deploy install-ipk

all: manager ipk

# ── vless-manager (with embedded sing-box) ────────────────────────────────────

manager:
	@mkdir -p $(BUILD_DIR)
	GOOS=$(GOOS) GOARCH=$(GOARCH) GOMIPS=$(GOMIPS) CGO_ENABLED=$(CGO) \
		go build -tags "$(BUILD_TAGS)" -trimpath -ldflags="$(LDFLAGS)" \
		-o $(BUILD_DIR)/vless-manager ./cmd/vless-manager/
	@echo "Built $(BUILD_DIR)/vless-manager $(VERSION) ($(shell du -h $(BUILD_DIR)/vless-manager | cut -f1))"
	@echo "NOTE: UPX segfaults on MT7621 — leaving uncompressed."

# ── IPK ───────────────────────────────────────────────────────────────────────

ipk: manager
	chmod +x packaging/build_ipk.sh
	VERSION=$(VERSION) ARCH=$(ARCH) packaging/build_ipk.sh

# OpenWrt IPK (mipsel_24kc). Doesn't bundle sing-box — depends on the
# `sing-box` package in the OpenWrt repo. Result is ~3 MB instead of ~30 MB.
ipk-openwrt: manager
	chmod +x packaging/openwrt/build_ipk.sh
	VERSION=$(VERSION) packaging/openwrt/build_ipk.sh

# Push the OpenWrt IPK to the router (root@192.168.201.1 -p 22). Assumes
# sing-box and the TPROXY kernel modules are already installed there.
ROUTER_OPENWRT      ?= root@192.168.201.1
PORT_OPENWRT        ?= 22
PASS_OPENWRT        ?=
install-ipk-openwrt:
	sshpass -p '$(PASS_OPENWRT)' scp -O -o StrictHostKeyChecking=no \
		-P $(PORT_OPENWRT) $(BUILD_DIR)/vless-manager_$(VERSION)_mipsel_24kc.ipk \
		$(ROUTER_OPENWRT):/tmp/
	sshpass -p '$(PASS_OPENWRT)' ssh -o StrictHostKeyChecking=no \
		-p $(PORT_OPENWRT) $(ROUTER_OPENWRT) \
		"opkg install --force-reinstall /tmp/vless-manager_$(VERSION)_mipsel_24kc.ipk && \
		 rm /tmp/vless-manager_$(VERSION)_mipsel_24kc.ipk"

# ── Deploy (direct SCP, no opkg) ──────────────────────────────────────────────

deploy: manager
	sshpass -p '$(PASS)' ssh -p $(PORT) $(ROUTER) "kill \$$(pidof vless-manager) 2>/dev/null; sleep 1; true"
	sshpass -p '$(PASS)' scp -O -P $(PORT) $(BUILD_DIR)/vless-manager  $(ROUTER):/opt/bin/vless-manager
	sshpass -p '$(PASS)' scp -O -P $(PORT) packaging/init.d/S99vless-manager $(ROUTER):/opt/etc/init.d/S99vless-manager
	sshpass -p '$(PASS)' ssh -p $(PORT) $(ROUTER) \
		"mkdir -p /opt/etc/vless-manager /opt/var/run /opt/var/log && \
		 chmod 755 /opt/bin/vless-manager /opt/etc/init.d/S99vless-manager && \
		 /opt/etc/init.d/S99vless-manager restart"

# ── Install via opkg ──────────────────────────────────────────────────────────

install-ipk:
	sshpass -p '$(PASS)' scp -O -P $(PORT) $(BUILD_DIR)/vless-manager_$(VERSION)_$(ARCH).ipk $(ROUTER):/tmp/
	sshpass -p '$(PASS)' ssh -p $(PORT) $(ROUTER) \
		"opkg install --force-reinstall /tmp/vless-manager_$(VERSION)_$(ARCH).ipk"

clean:
	rm -rf $(BUILD_DIR)
