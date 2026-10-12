//go:build linux && mipsle && gc && !purego

package chacha20

import (
	"os"
	"strings"
)

const bufSize = blockSize

var useMIPS32R2 = detectMIPS32R2()

func detectMIPS32R2() bool {
	if os.Getenv("VLESS_MANAGER_MIPS_CHACHA20") == "generic" {
		return false
	}
	info, err := os.ReadFile("/proc/cpuinfo")
	return err == nil && supportsMIPS32R2(string(info))
}

func supportsMIPS32R2(info string) bool {
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
			// r6 removed the unaligned load/store instructions used below.
			if !r2 || r6 {
				return false
			}
			supported++
		}
	}
	return processors > 0 && supported == processors
}

//go:noescape
func xorBlockMIPS32R2(state *[16]uint32, dst, src *byte)

//go:noescape
func xorBlocksMIPS32R2(state *[16]uint32, dst, src *byte, blocks uint32)

// Bound leaf-call time so large streams remain preemptible between batches.
const maxMIPSBatch = 16 * 1024

func (s *Cipher) xorKeyStreamBlocks(dst, src []byte) {
	if !useMIPS32R2 {
		s.xorKeyStreamBlocksGeneric(dst, src)
		return
	}
	if len(dst) != len(src) || len(src)%blockSize != 0 {
		panic("chacha20: internal error: wrong dst and/or src length")
	}
	state := [16]uint32{j0, j1, j2, j3,
		s.key[0], s.key[1], s.key[2], s.key[3],
		s.key[4], s.key[5], s.key[6], s.key[7],
		s.counter, s.nonce[0], s.nonce[1], s.nonce[2]}
	// Upstream XORKeyStream still owns overlap, buffering and overflow checks.
	for len(src) > 0 {
		n := min(len(src), maxMIPSBatch)
		blocks := uint32(n / blockSize)
		xorBlocksMIPS32R2(&state, &dst[0], &src[0], blocks)
		state[12] += blocks
		src, dst = src[n:], dst[n:]
	}
	s.counter = state[12]
}
