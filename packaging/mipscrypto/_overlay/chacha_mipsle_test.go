//go:build linux && mipsle && gc && !purego

package chacha20

import (
	"bytes"
	"fmt"
	"math/rand"
	"os"
	"runtime"
	"sync"
	"syscall"
	"testing"
)

func TestMIPS32R2Detection(t *testing.T) {
	for _, tc := range []struct {
		info string
		want bool
	}{
		{"", false},
		{"processor : 0\nisa : mips1 mips32r1\n", false},
		{"processor : 0\nisa : mips1 mips32r1 mips32r2\n", true},
		{"processor : 0\nisa : mips32r2\nprocessor : 1\nisa : mips32r2\n", true},
		{"processor : 0\nisa : mips32r2\nprocessor : 1\nisa : mips32r1\n", false},
		{"processor : 0\nisa : mips32r2\nprocessor : 1\n", false},
		{"processor : 0\nisa : mips32r2 mips32r6\n", false},
		{"processor : 0\nisa : not-mips32r2\n", false},
		{"isa : mips32r2\n", false},
	} {
		if got := supportsMIPS32R2(tc.info); got != tc.want {
			t.Errorf("supportsMIPS32R2(%q) = %v, want %v", tc.info, got, tc.want)
		}
	}
	t.Setenv("VLESS_MANAGER_MIPS_CHACHA20", "generic")
	if detectMIPS32R2() {
		t.Fatal("explicit generic fallback ignored")
	}
}

func TestMIPSAssemblyAgainstGeneric(t *testing.T) {
	info, err := os.ReadFile("/proc/cpuinfo")
	if err != nil || !supportsMIPS32R2(string(info)) {
		t.Skip("CPU does not advertise MIPS32r2")
	}
	if !useMIPS32R2 {
		t.Skip("generic fallback requested")
	}
	rng := rand.New(rand.NewSource(8439))
	for trial := 0; trial < 1000; trial++ {
		var state [16]uint32
		for i := range state {
			state[i] = rng.Uint32()
		}
		state[0], state[1], state[2], state[3] = j0, j1, j2, j3
		for srcOffset := 0; srcOffset < 4; srcOffset++ {
			for dstOffset := 0; dstOffset < 4; dstOffset++ {
				src, dst := make([]byte, 72), bytes.Repeat([]byte{0xa5}, 72)
				rng.Read(src)
				original := append([]byte(nil), src...)
				generic := Cipher{counter: state[12]}
				copy(generic.key[:], state[4:12])
				copy(generic.nonce[:], state[13:])
				want := make([]byte, 64)
				generic.xorKeyStreamBlocksGeneric(want, src[srcOffset:srcOffset+64])
				xorBlockMIPS32R2(&state, &dst[dstOffset], &src[srcOffset])
				if !bytes.Equal(dst[dstOffset:dstOffset+64], want) || !bytes.Equal(src, original) {
					t.Fatalf("differential mismatch trial=%d src=%d dst=%d", trial, srcOffset, dstOffset)
				}
				if !bytes.Equal(dst[:dstOffset], bytes.Repeat([]byte{0xa5}, dstOffset)) ||
					!bytes.Equal(dst[dstOffset+64:], bytes.Repeat([]byte{0xa5}, 8-dstOffset)) {
					t.Fatal("assembly wrote outside destination")
				}
				xorBlockMIPS32R2(&state, &src[srcOffset], &src[srcOffset])
				if !bytes.Equal(src[srcOffset:srcOffset+64], want) {
					t.Fatal("in-place assembly mismatch")
				}
			}
		}
	}
}

func TestMIPSAssemblyBackend(t *testing.T) {
	info, err := os.ReadFile("/proc/cpuinfo")
	want := err == nil && supportsMIPS32R2(string(info)) && os.Getenv("VLESS_MANAGER_MIPS_CHACHA20") != "generic"
	if want != useMIPS32R2 {
		t.Fatalf("assembly dispatch = %v, want %v", useMIPS32R2, want)
	}
	t.Logf("MIPS32r2 assembly enabled: %v", useMIPS32R2)
}

func TestMIPSAssemblyBatch(t *testing.T) {
	if !useMIPS32R2 {
		t.Skip("assembly unavailable")
	}
	xorBlocksMIPS32R2(nil, nil, nil, 0)
	rng := rand.New(rand.NewSource(15781))
	for trial := 0; trial < 64; trial++ {
		var state [16]uint32
		for i := range state {
			state[i] = rng.Uint32()
		}
		state[0], state[1], state[2], state[3] = j0, j1, j2, j3
		blocks := 1 + rng.Intn(33)
		// Exercise carry and the valid final block without generating past it.
		if trial%8 == 0 {
			state[12] = ^uint32(0) - uint32(blocks-1)
		}
		originalState := state
		for srcOffset := 0; srcOffset < 4; srcOffset++ {
			for dstOffset := 0; dstOffset < 4; dstOffset++ {
				size := blocks * blockSize
				src, dst := make([]byte, size+8), bytes.Repeat([]byte{0xa5}, size+8)
				rng.Read(src)
				original := append([]byte(nil), src...)
				generic := Cipher{counter: state[12]}
				copy(generic.key[:], state[4:12])
				copy(generic.nonce[:], state[13:])
				want := make([]byte, size)
				generic.xorKeyStreamBlocksGeneric(want, src[srcOffset:srcOffset+size])
				xorBlocksMIPS32R2(&state, &dst[dstOffset], &src[srcOffset], uint32(blocks))
				if state != originalState || !bytes.Equal(dst[dstOffset:dstOffset+size], want) || !bytes.Equal(src, original) {
					t.Fatalf("batch mismatch trial=%d src=%d dst=%d blocks=%d", trial, srcOffset, dstOffset, blocks)
				}
				if !bytes.Equal(dst[:dstOffset], bytes.Repeat([]byte{0xa5}, dstOffset)) ||
					!bytes.Equal(dst[dstOffset+size:], bytes.Repeat([]byte{0xa5}, 8-dstOffset)) {
					t.Fatal("batch wrote outside destination")
				}
				xorBlocksMIPS32R2(&state, &src[srcOffset], &src[srcOffset], uint32(blocks))
				if !bytes.Equal(src[srcOffset:srcOffset+size], want) || state != originalState {
					t.Fatal("in-place batch mismatch")
				}
			}
		}
	}
	for _, size := range []int{maxMIPSBatch - blockSize, maxMIPSBatch, maxMIPSBatch + blockSize, 2*maxMIPSBatch + 1} {
		src, want, got := make([]byte, size), make([]byte, ((size+blockSize-1)/blockSize)*blockSize), make([]byte, size)
		rng.Read(src)
		var generic, assembly Cipher
		copy(want, src)
		generic.xorKeyStreamBlocksGeneric(want, want)
		assembly.XORKeyStream(got, src)
		if !bytes.Equal(got, want[:size]) || assembly.counter != generic.counter {
			t.Fatalf("wrapper batch boundary mismatch size=%d", size)
		}
	}
}

func TestMIPSAssemblyPageBoundary(t *testing.T) {
	if !useMIPS32R2 {
		t.Skip("assembly unavailable")
	}
	page := os.Getpagesize()
	memory := func() []byte {
		data, err := syscall.Mmap(-1, 0, page*3, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_PRIVATE|syscall.MAP_ANON)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { syscall.Munmap(data) })
		if err := syscall.Mprotect(data[:page], syscall.PROT_NONE); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Mprotect(data[page*2:], syscall.PROT_NONE); err != nil {
			t.Fatal(err)
		}
		return data[page : page*2]
	}
	src, dst := memory(), memory()
	state := [16]uint32{j0, j1, j2, j3}
	for _, size := range []int{64, 128, 1024} {
		for _, start := range []int{0, 1, 2, 3, page - size - 3, page - size - 2, page - size - 1, page - size} {
			block := src[start : start+size]
			for i := range block {
				block[i] = byte(i)
			}
			want := make([]byte, size)
			generic := Cipher{}
			generic.xorKeyStreamBlocksGeneric(want, block)
			xorBlocksMIPS32R2(&state, &dst[start], &src[start], uint32(size/blockSize))
			if !bytes.Equal(want, dst[start:start+size]) {
				t.Fatalf("page boundary output mismatch size=%d start=%d", size, start)
			}
		}
	}
}

func TestMIPSAssemblyConcurrent(t *testing.T) {
	if !useMIPS32R2 {
		t.Skip("assembly unavailable")
	}
	var workers sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		workers.Go(func() {
			rng := rand.New(rand.NewSource(int64(worker + 1)))
			src, want, got := make([]byte, 16384), make([]byte, 16384), make([]byte, 16384)
			for trial := 0; trial < 128; trial++ {
				rng.Read(src)
				cipher := Cipher{counter: rng.Uint32() & 0x00ffffff}
				for i := range cipher.key {
					cipher.key[i] = rng.Uint32()
				}
				for i := range cipher.nonce {
					cipher.nonce[i] = rng.Uint32()
				}
				generic := cipher
				generic.xorKeyStreamBlocksGeneric(want, src)
				cipher.XORKeyStream(got, src)
				if !bytes.Equal(got, want) || cipher.counter != generic.counter {
					t.Errorf("concurrent cipher mismatch worker=%d trial=%d", worker, trial)
					return
				}
				if trial%16 == 0 {
					runtime.GC()
				}
			}
		})
	}
	workers.Wait()
}

func BenchmarkMIPSChaCha(b *testing.B) {
	for _, size := range []int{64, 1024, 16384} {
		for _, name := range []string{"assembly", "legacy_calls", "generic"} {
			b.Run(fmt.Sprintf("%s/%d", name, size), func(b *testing.B) {
				if name != "generic" && !useMIPS32R2 {
					b.Skip("assembly unavailable")
				}
				cipher, _ := NewUnauthenticatedCipher(make([]byte, KeySize), make([]byte, NonceSize))
				src, dst := make([]byte, size), make([]byte, size)
				b.SetBytes(int64(size))
				b.ReportAllocs()
				b.ResetTimer()
				for n := 0; n < b.N; n++ {
					cipher.counter = 0
					if name == "generic" {
						cipher.xorKeyStreamBlocksGeneric(dst, src)
					} else if name == "legacy_calls" {
						xorLegacyMIPSBlocks(cipher, dst, src)
					} else {
						cipher.xorKeyStreamBlocks(dst, src)
					}
				}
			})
		}
	}
}

func xorLegacyMIPSBlocks(s *Cipher, dst, src []byte) {
	if len(dst) != len(src) || len(src)%blockSize != 0 {
		panic("invalid legacy benchmark length")
	}
	state := [16]uint32{j0, j1, j2, j3,
		s.key[0], s.key[1], s.key[2], s.key[3],
		s.key[4], s.key[5], s.key[6], s.key[7],
		s.counter, s.nonce[0], s.nonce[1], s.nonce[2]}
	for len(src) >= blockSize {
		xorBlockMIPS32R2(&state, &dst[0], &src[0])
		state[12]++
		src, dst = src[blockSize:], dst[blockSize:]
	}
	s.counter = state[12]
}

func BenchmarkMIPSChaChaParallel(b *testing.B) {
	if !useMIPS32R2 {
		b.Skip("assembly unavailable")
	}
	b.SetBytes(16384)
	b.ReportAllocs()
	b.RunParallel(func(p *testing.PB) {
		cipher, _ := NewUnauthenticatedCipher(make([]byte, KeySize), make([]byte, NonceSize))
		src, dst := make([]byte, 16384), make([]byte, 16384)
		for p.Next() {
			cipher.counter = 0
			cipher.xorKeyStreamBlocks(dst, src)
		}
	})
}
