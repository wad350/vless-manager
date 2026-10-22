package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitCryptoDefaults(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "init.d", "S99vless-manager"))
	if err != nil {
		t.Fatal(err)
	}
	prefix, _, ok := strings.Cut(string(data), "\nBIN=")
	if !ok {
		t.Fatal("init profile boundary not found")
	}
	dir := t.TempDir()
	for name, source := range map[string]string{
		"uname": "#!/bin/sh\nprintf '%s\\n' \"$TEST_MACHINE\"\n",
		"awk":   "#!/bin/sh\nprintf '%s\\n' \"$TEST_CPU_LIMIT\"\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(source), 0755); err != nil {
			t.Fatal(err)
		}
	}
	prefix = strings.Replace(prefix, "export PATH=", "export PATH="+dir+":", 1)
	for _, tc := range []struct{ name, machine, limit, override, want string }{
		{"mt7621", "mips", "2", "", "auto/macc/2"},
		{"unknown_mips", "mips", "", "", "generic/unset/unset"},
		{"arm64", "aarch64", "2", "", "unset/unset/unset"},
		{"poly_override", "mips", "2", "VLESS_MANAGER_MIPS_POLY1305=generic\n", "auto/generic/2"},
		{"poly_previous_assembly", "mips", "2", "VLESS_MANAGER_MIPS_POLY1305=auto\n", "auto/auto/2"},
		{"chacha_override", "mips", "2", "VLESS_MANAGER_MIPS_CHACHA20=generic\n", "generic/macc/2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command("sh", "-c", "unset VLESS_MANAGER_MIPS_CHACHA20 VLESS_MANAGER_MIPS_POLY1305 GOMAXPROCS\n"+tc.override+prefix+"\nprintf '%s/%s/%s' \"${VLESS_MANAGER_MIPS_CHACHA20-unset}\" \"${VLESS_MANAGER_MIPS_POLY1305-unset}\" \"${GOMAXPROCS-unset}\"")
			cmd.Env = append(os.Environ(), "TEST_MACHINE="+tc.machine, "TEST_CPU_LIMIT="+tc.limit)
			got, err := cmd.CombinedOutput()
			if err != nil || string(got) != tc.want {
				t.Fatalf("init profile = %q, %v, want %q", got, err, tc.want)
			}
		})
	}
}
