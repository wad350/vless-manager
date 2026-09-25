# ── Project metadata ──────────────────────────────────────────────────────────
VERSION      := 1.17.0
ARCH         ?= aarch64-3.10
BUILD_DATE   := $(shell date -u +%Y-%m-%d)

# ── Embedded Xray ─────────────────────────────────────────────────────────
# Xray v26.9.9 prerelease, pinned by commit in go.mod.
XRAY_TAG  := v26.9.9+manager.1
UPDATE_REPOSITORY ?= wad350/vless-manager

# ── Go cross-compile target (Keenetic MT7621 = mipsle softfloat) ──────────────
GOOS         ?= linux
GOARCH       ?= arm64
GOMIPS       ?= softfloat
CGO          ?= 0
# Pin the toolchain required by the bundled Xray source.
GOTOOLCHAIN  := go1.27.1
export GOTOOLCHAIN
LDFLAGS      := -s -w \
                -X main.Version=$(VERSION) \
                -X main.BuildDate=$(BUILD_DATE) \
                -X main.BundledXray=$(XRAY_TAG) \
                -X main.UpdateRepository=$(UPDATE_REPOSITORY)

# ── Paths ─────────────────────────────────────────────────────────────────────
BUILD_DIR    := build

# ── Router deploy (direct SCP) ────────────────────────────────────────────────
ROUTER       ?= root@192.168.13.1
PORT         ?= 222
PASS         ?=

.PHONY: all manager ipk clean deploy install-ipk

all: manager ipk

# ── vless-manager (with embedded Xray) ────────────────────────────────────

manager:
	@mkdir -p $(BUILD_DIR)
	GOOS=$(GOOS) GOARCH=$(GOARCH) GOMIPS=$(GOMIPS) CGO_ENABLED=$(CGO) \
		go build -trimpath -ldflags="$(LDFLAGS)" \
		-o $(BUILD_DIR)/vless-manager ./cmd/vless-manager/
	@echo "Built $(BUILD_DIR)/vless-manager $(VERSION) ($$(du -h $(BUILD_DIR)/vless-manager | cut -f1))"
	@echo "NOTE: UPX segfaults on MT7621 — leaving uncompressed."

# ── IPK ───────────────────────────────────────────────────────────────────────

ipk: manager
	chmod +x packaging/build_ipk.sh
	VERSION=$(VERSION) ARCH=$(ARCH) BUILD_DIR=$(BUILD_DIR) packaging/build_ipk.sh

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
