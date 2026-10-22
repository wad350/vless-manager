//go:build linux && mipsle && gc && !purego

package poly1305

import (
	"os"
	"strings"
)

var useMIPSPoly1305 = detectMIPSPoly1305()
var useMIPSMACC = useMIPSPoly1305 && os.Getenv("VLESS_MANAGER_MIPS_POLY1305") == "macc"

func detectMIPSPoly1305() bool {
	mode := os.Getenv("VLESS_MANAGER_MIPS_POLY1305")
	if (mode != "auto" && mode != "macc") || os.Getenv("VLESS_MANAGER_MIPS_CHACHA20") == "generic" {
		return false
	}
	data, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return false
	}
	return supportsMIPSPoly1305(string(data))
}

func supportsMIPSPoly1305(info string) bool {
	processors, supported := 0, 0
	for _, line := range strings.Split(info, "\n") {
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

// productsMIPS requires c <= 7 and clamped r/s, as guaranteed by updateProductsMIPS.
//
//go:noescape
func productsMIPS(a, b, c, r, s uint64) (ar, br, cr, as, bs, cs uint128)

//go:noescape
func polyBlocksMIPS(state *macState, msg *byte, blocks uint32)

//go:noescape
func polyBlocksMACCMIPS(state *macState, msg *byte, blocks uint32)

func updateMIPS(state *macState, msg []byte) {
	for len(msg) >= TagSize {
		n := len(msg) & ^(TagSize - 1)
		if n > 16*1024 {
			n = 16 * 1024
		}
		if useMIPSMACC {
			polyBlocksMACCMIPS(state, &msg[0], uint32(n/TagSize))
		} else {
			polyBlocksMIPS(state, &msg[0], uint32(n/TagSize))
		}
		msg = msg[n:]
	}
	if len(msg) != 0 {
		updateGenericGo(state, msg)
	}
}

func updateGeneric(state *macState, msg []byte) {
	if useMIPSPoly1305 {
		updateMIPS(state, msg)
	} else {
		updateGenericGo(state, msg)
	}
}
