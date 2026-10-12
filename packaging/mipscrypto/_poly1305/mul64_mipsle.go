//go:build linux && mipsle && gc && !purego

package poly1305

import (
	"os"
	"strings"
)

var useMIPSMul64 = detectMIPSMul64()

func detectMIPSMul64() bool {
	if os.Getenv("VLESS_MANAGER_MIPS_POLY1305") != "auto" || os.Getenv("VLESS_MANAGER_MIPS_CHACHA20") == "generic" {
		return false
	}
	data, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return false
	}
	processors, supported := 0, 0
	for _, line := range strings.Split(string(data), "\n") {
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(name) {
		case "processor":
			processors++
		case "isa":
			r2, r6 := false, false
			for _, isa := range strings.Fields(value) {
				r2 = r2 || isa == "mips32r2"
				r6 = r6 || isa == "mips32r6"
			}
			if !r2 || r6 {
				return false
			}
			supported++
		}
	}
	return processors > 0 && processors == supported
}

//go:noescape
func mul64MIPS(a, b uint64) (lo, hi uint64)

// productsMIPS requires c <= 7 and clamped r/s, as guaranteed by updateMIPS.
//
//go:noescape
func productsMIPS(a, b, c, r, s uint64) (ar, br, cr, as, bs, cs uint128)

func updateGeneric(state *macState, msg []byte) {
	if useMIPSMul64 {
		updateMIPS(state, msg)
	} else {
		updateGenericGo(state, msg)
	}
}
