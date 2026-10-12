package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitScriptMemoryProfile(t *testing.T) {
	script, err := filepath.Abs("../../packaging/init.d/S99vless-manager")
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name, machine, memoryLimit, gc, want string
	}{
		{"mips defaults", "mips", "", "", "32MiB|50"},
		{"mipsel defaults", "mipsel", "", "", "32MiB|50"},
		{"mips explicit settings", "mips", "48MiB", "75", "48MiB|75"},
		{"mips explicit zero gc", "mips", "32MiB", "0", "32MiB|0"},
		{"arm64 defaults", "aarch64", "", "", "unset|unset"},
		{"arm64 explicit settings", "aarch64", "64MiB", "100", "64MiB|100"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cmd := exec.Command("sh", "-c", `
uname() { printf '%s\n' "$TEST_MACHINE"; }
init_script=$1
set -- status
. "$init_script" >/dev/null
printf '%s|%s' "${GOMEMLIMIT-unset}" "${GOGC-unset}"
`, "init-test", script)
			for _, entry := range os.Environ() {
				if !strings.HasPrefix(entry, "GOMEMLIMIT=") && !strings.HasPrefix(entry, "GOGC=") && !strings.HasPrefix(entry, "TEST_MACHINE=") {
					cmd.Env = append(cmd.Env, entry)
				}
			}
			cmd.Env = append(cmd.Env, "TEST_MACHINE="+tt.machine)
			if tt.memoryLimit != "" {
				cmd.Env = append(cmd.Env, "GOMEMLIMIT="+tt.memoryLimit)
			}
			if tt.gc != "" {
				cmd.Env = append(cmd.Env, "GOGC="+tt.gc)
			}
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("init script: %v: %s", err, output)
			}
			if string(output) != tt.want {
				t.Fatalf("memory profile = %q, want %q", output, tt.want)
			}
		})
	}
}

func TestInitRuntimeFilesStayInRAM(t *testing.T) {
	script, err := filepath.Abs("../../packaging/init.d/S99vless-manager")
	if err != nil {
		t.Fatal(err)
	}
	ramDir := t.TempDir()
	cmd := exec.Command("sh", "-c", `
init_script=$1
set -- status
. "$init_script" >/dev/null
printf '%s|%s|%s' "$PID_FILE" "$LOG_FILE" "$RUNTIME_DIR"
`, "init-runtime-test", script)
	cmd.Env = append(os.Environ(), "VLESS_MANAGER_RUNTIME_DIR="+ramDir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("init runtime: %v: %s", err, output)
	}
	want := ramDir + "/manager.pid|" + ramDir + "/process.log|" + ramDir
	if string(output) != want {
		t.Fatalf("runtime files = %q, want %q", output, want)
	}
}

func TestInitCleansKernelTCPAfterCrash(t *testing.T) {
	script, err := filepath.Abs("../../packaging/init.d/S99vless-manager")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-c", `
iptables() { printf 'iptables %s\n' "$*"; case " $* " in *" -D "*) return 1;; esac; }
ip() { return 1; }
init_script=$1
set -- status
. "$init_script" >/dev/null
clean_routing
true
`, "init-cleanup-test", script)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("cleanup: %v: %s", err, output)
	}
	for _, required := range []string{
		"-t mangle -D OUTPUT -j VLESS_OUTPUT", "-t mangle -D PREROUTING -i lo -j VLESS_TPROXY",
		"-D INPUT -j VLESS_IN", "-t mangle -X VLESS_OUTPUT", "-X VLESS_IN",
		"-t mangle -D INPUT -j VLESS_IN", "-t mangle -X VLESS_IN",
		"-t nat -D PREROUTING -i br0 -p tcp -j VLESS_TCP", "-t nat -D PREROUTING -i br-lan -p tcp -j VLESS_TCP",
		"-t nat -D OUTPUT -p tcp -j VLESS_TCP", "-D INPUT -p tcp --dport 7892 -j VLESS_TCP_IN",
		"-t nat -X VLESS_TCP", "-X VLESS_TCP_IN",
	} {
		if !strings.Contains(string(output), required) {
			t.Fatal("missing cleanup " + required)
		}
	}
}

func TestInitUnknownMIPSCryptoRequiresOptIn(t *testing.T) {
	script, err := filepath.Abs("../../packaging/init.d/S99vless-manager")
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name, machine, override, want string
	}{
		{"MIPS safe default", "mips", "", "generic"},
		{"MIPSEL safe default", "mipsel", "", "generic"},
		{"explicit acceleration", "mips", "auto", "auto"},
		{"explicit fallback", "mips", "generic", "generic"},
		{"ARM unchanged", "aarch64", "", "unset"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cmd := exec.Command("sh", "-c", `
uname() { printf '%s\n' "$TEST_MACHINE"; }
awk() { return 0; }
init_script=$1
set -- status
. "$init_script" >/dev/null
printf '%s' "${VLESS_MANAGER_MIPS_CHACHA20-unset}"
`, "init-crypto-test", script)
			for _, entry := range os.Environ() {
				if !strings.HasPrefix(entry, "VLESS_MANAGER_MIPS_CHACHA20=") && !strings.HasPrefix(entry, "TEST_MACHINE=") {
					cmd.Env = append(cmd.Env, entry)
				}
			}
			cmd.Env = append(cmd.Env, "TEST_MACHINE="+tt.machine)
			if tt.override != "" {
				cmd.Env = append(cmd.Env, "VLESS_MANAGER_MIPS_CHACHA20="+tt.override)
			}
			output, err := cmd.CombinedOutput()
			if err != nil || string(output) != tt.want {
				t.Fatalf("crypto backend = %q, error %v, want %q", output, err, tt.want)
			}
		})
	}
}

func TestInitMT7621CPUProfile(t *testing.T) {
	script, err := filepath.Abs("../../packaging/init.d/S99vless-manager")
	if err != nil {
		t.Fatal(err)
	}
	fixture := func(soc string, cores ...int) string {
		var out strings.Builder
		fmt.Fprintf(&out, "system type\t: %s\n", soc)
		for logical, core := range cores {
			fmt.Fprintf(&out, "processor\t: %d\ncore\t\t: %d\nVPE\t\t: %d\n\n", logical, core, logical%2)
		}
		return out.String()
	}
	mt7621 := fixture("MediaTek MT7621 SoC", 0, 0, 1, 1)
	for _, tt := range []struct {
		name, machine, cpuinfo, override, want string
	}{
		{"MT7621 two cores", "mips", mt7621, "", "2"},
		{"MT7621 mipsel", "mipsel", mt7621, "", "2"},
		{"explicit four workers", "mips", mt7621, "4", "4"},
		{"explicit one worker", "mips", mt7621, "1", "1"},
		{"explicit zero unchanged", "mips", mt7621, "0", "0"},
		{"single core unchanged", "mips", fixture("MediaTek MT7621 SoC", 0, 0), "", "unset"},
		{"four cores unchanged", "mips", fixture("MediaTek MT7621 SoC", 0, 1, 2, 3), "", "unset"},
		{"other MIPS unchanged", "mips", fixture("MediaTek MT7620 SoC", 0, 0, 1, 1), "", "unset"},
		{"missing topology unchanged", "mips", "system type: MediaTek MT7621 SoC\n", "", "unset"},
		{"missing CPU data unchanged", "mips", "", "", "unset"},
		{"ARM unchanged", "aarch64", mt7621, "", "unset"},
		{"ARM explicit unchanged", "aarch64", mt7621, "4", "4"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "cpuinfo")
			if err := os.WriteFile(path, []byte(tt.cpuinfo), 0600); err != nil {
				t.Fatal(err)
			}
			for _, cryptoOverride := range []string{"", "generic", "auto"} {
				cmd := exec.Command("sh", "-c", `
uname() { printf '%s\n' "$TEST_MACHINE"; }
awk() { command awk "$1" "$TEST_CPUINFO"; }
init_script=$1
set -- status
. "$init_script" >/dev/null
printf '%s|%s' "${GOMAXPROCS-unset}" "${VLESS_MANAGER_MIPS_CHACHA20-unset}"
`, "init-cpu-test", script)
				for _, entry := range os.Environ() {
					if !strings.HasPrefix(entry, "GOMAXPROCS=") && !strings.HasPrefix(entry, "VLESS_MANAGER_MIPS_CHACHA20=") && !strings.HasPrefix(entry, "TEST_MACHINE=") && !strings.HasPrefix(entry, "TEST_CPUINFO=") {
						cmd.Env = append(cmd.Env, entry)
					}
				}
				cmd.Env = append(cmd.Env, "TEST_MACHINE="+tt.machine, "TEST_CPUINFO="+path)
				if tt.override != "" {
					cmd.Env = append(cmd.Env, "GOMAXPROCS="+tt.override)
				}
				wantCrypto := cryptoOverride
				if cryptoOverride != "" {
					cmd.Env = append(cmd.Env, "VLESS_MANAGER_MIPS_CHACHA20="+cryptoOverride)
				} else if strings.HasPrefix(tt.machine, "mips") {
					wantCrypto = "generic"
					if tt.cpuinfo == mt7621 {
						wantCrypto = "auto"
					}
				} else {
					wantCrypto = "unset"
				}
				output, err := cmd.CombinedOutput()
				want := tt.want + "|" + wantCrypto
				if err != nil || string(output) != want {
					t.Fatalf("CPU/crypto profile = %q, error %v, want %q", output, err, want)
				}
			}
		})
	}
}
