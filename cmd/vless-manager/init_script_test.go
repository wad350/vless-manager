package main

import (
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
