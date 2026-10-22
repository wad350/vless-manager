//go:build linux && mipsle && gc && !purego

package poly1305

import (
	"fmt"
	"math/bits"
	"math/rand"
	"os"
	"runtime"
	"sync"
	"syscall"
	"testing"
	"unsafe"
)

func updateMACCMIPS(state *macState, msg []byte) {
	for len(msg) >= TagSize {
		n := min(len(msg)&^(TagSize-1), 16*1024)
		polyBlocksMACCMIPS(state, &msg[0], uint32(n/TagSize))
		msg = msg[n:]
	}
	if len(msg) != 0 {
		updateGenericGo(state, msg)
	}
}

func TestMIPSPolyBackend(t *testing.T) {
	for _, tc := range []struct {
		info string
		want bool
	}{
		{"processor: 0\nisa: mips1 mips32r2\n", true},
		{"processor: 0\nisa: mips32r2\nprocessor: 1\nisa: mips32r2\n", true},
		{"", false},
		{"processor: 0\n", false},
		{"processor: 0\nisa: mips32r1\n", false},
		{"processor: 0\nisa: mips32r2 mips32r6\n", false},
		{"processor: 0\nisa: mips32r2\nprocessor: 1\nisa: mips32r1\n", false},
	} {
		if got := supportsMIPSPoly1305(tc.info); got != tc.want {
			t.Fatalf("capability %q = %v, want %v", tc.info, got, tc.want)
		}
	}
	info, err := os.ReadFile("/proc/cpuinfo")
	mode := os.Getenv("VLESS_MANAGER_MIPS_POLY1305")
	want := err == nil && supportsMIPSPoly1305(string(info)) && (mode == "auto" || mode == "macc") && os.Getenv("VLESS_MANAGER_MIPS_CHACHA20") != "generic"
	if useMIPSPoly1305 != want {
		t.Fatalf("backend = %v, want %v", useMIPSPoly1305, want)
	}
	if useMIPSMACC != (want && mode == "macc") {
		t.Fatal("MACC backend selection does not match the explicit opt-in")
	}
	t.Setenv("VLESS_MANAGER_MIPS_POLY1305", "generic")
	if detectMIPSPoly1305() {
		t.Fatal("generic override did not disable assembly")
	}
	t.Setenv("VLESS_MANAGER_MIPS_POLY1305", "auto")
	t.Setenv("VLESS_MANAGER_MIPS_CHACHA20", "generic")
	if detectMIPSPoly1305() {
		t.Fatal("ChaCha generic override did not disable assembly")
	}
}

func TestMIPSMultiply(t *testing.T) {
	edges := []uint64{0, 1, 2, 0x7fffffff, 0x80000000, 0xffffffff, 0x100000000, 0x100000001, 0x7fffffffffffffff, 0x8000000000000000, ^uint64(0)}
	check := func(a, b uint64) {
		t.Helper()
		lo, hi := mul64MIPS(a, b)
		wantHi, wantLo := bits.Mul64(a, b)
		if lo != wantLo || hi != wantHi {
			t.Fatalf("%016x * %016x = %016x:%016x, want %016x:%016x", a, b, hi, lo, wantHi, wantLo)
		}
	}
	for _, a := range edges {
		for _, b := range edges {
			check(a, b)
		}
	}
	rng := rand.New(rand.NewSource(1305))
	for i := 0; i < 100000; i++ {
		check(rng.Uint64(), rng.Uint64())
	}
	for i := 0; i < 20000; i++ {
		a, b, c, r, s := rng.Uint64(), rng.Uint64(), rng.Uint64()%8, rng.Uint64()&rMask0, rng.Uint64()&rMask1
		ar, br, cr, as, bs, cs := productsMIPS(a, b, c, r, s)
		got := [6]uint128{ar, br, cr, as, bs, cs}
		want := [6]uint128{mul64(a, r), mul64(b, r), mul64(c, r), mul64(a, s), mul64(b, s), mul64(c, s)}
		if got != want {
			t.Fatalf("fused products mismatch: got %+v, want %+v", got, want)
		}
	}
	for c := uint64(0); c <= 7; c++ {
		ar, br, cr, as, bs, cs := productsMIPS(^uint64(0), ^uint64(0), c, rMask0, rMask1)
		got := [6]uint128{ar, br, cr, as, bs, cs}
		want := [6]uint128{mul64(^uint64(0), rMask0), mul64(^uint64(0), rMask0), mul64(c, rMask0), mul64(^uint64(0), rMask1), mul64(^uint64(0), rMask1), mul64(c, rMask1)}
		if got != want {
			t.Fatalf("maximal clamped products mismatch at c=%d", c)
		}
	}
}

func TestMIPSUpdate(t *testing.T) {
	rng := rand.New(rand.NewSource(1306))
	lengths := []int{0, 1, 15, 16, 17, 31, 32, 33, 64, 127, 1200, 16384, 16385, 32768}
	for trial := 0; trial < 32; trial++ {
		var key [32]byte
		rng.Read(key[:])
		for _, n := range lengths {
			for alignment := 0; alignment < 8; alignment++ {
				msg := make([]byte, n+alignment)[alignment:]
				rng.Read(msg)
				var initial macState
				initialize(&key, &initial)
				initial.h = [3]uint64{rng.Uint64(), rng.Uint64(), rng.Uint64() % 6}
				got, want := initial, initial
				updateMIPS(&got, msg)
				updateGenericGo(&want, msg)
				if got != want {
					t.Fatalf("trial=%d length=%d alignment=%d: got %+v, want %+v", trial, n, alignment, got, want)
				}
				candidate := initial
				updateMACCMIPS(&candidate, msg)
				if candidate != want {
					t.Fatalf("MACC trial=%d length=%d alignment=%d: got %+v, want %+v", trial, n, alignment, candidate, want)
				}
			}
		}
	}
}

func TestMIPSBlocksStateAndCarries(t *testing.T) {
	var layout macState
	if unsafe.Offsetof(layout.r) != 24 || unsafe.Offsetof(layout.s) != 40 || unsafe.Sizeof(layout.h) != 24 {
		t.Fatal("assembly macState offsets no longer match")
	}
	for _, keyWord := range []uint64{0, 1, 0x5555555555555555, 0xaaaaaaaaaaaaaaaa, ^uint64(0)} {
		for high := uint64(0); high <= 5; high++ {
			for _, word := range []byte{0, 1, 0x55, 0xaa, 0xff} {
				state := macState{h: [3]uint64{^uint64(0), ^uint64(0), high}, r: [2]uint64{keyWord & rMask0, keyWord & rMask1}}
				want := state
				candidate := state
				msg := make([]byte, 32768)
				for i := range msg {
					msg[i] = word
				}
				polyBlocksMIPS(&state, nil, 0)
				polyBlocksMACCMIPS(&candidate, nil, 0)
				if state != want || candidate != want {
					t.Fatal("zero blocks changed state")
				}
				updateMIPS(&state, msg)
				updateMACCMIPS(&candidate, msg)
				updateGenericGo(&want, msg)
				if state != want || candidate != want {
					t.Fatalf("maximal carries: r=%x high=%d message=%x: got %+v want %+v", keyWord, high, word, state, want)
				}
			}
		}
	}
}

func TestMIPSBlocksPageBoundary(t *testing.T) {
	page := syscall.Getpagesize()
	memory, err := syscall.Mmap(-1, 0, 3*page, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_PRIVATE|syscall.MAP_ANON)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Munmap(memory)
	if err := syscall.Mprotect(memory[:page], syscall.PROT_NONE); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mprotect(memory[2*page:], syscall.PROT_NONE); err != nil {
		t.Fatal(err)
	}
	rng := rand.New(rand.NewSource(1309))
	rng.Read(memory[page : 2*page])
	var key [32]byte
	rng.Read(key[:])
	for n := 1; n <= 64; n++ {
		for _, msg := range [][]byte{memory[page : page+n], memory[2*page-n : 2*page]} {
			var got macState
			initialize(&key, &got)
			want := got
			candidate := got
			updateMIPS(&got, msg)
			updateMACCMIPS(&candidate, msg)
			updateGenericGo(&want, msg)
			if got != want || candidate != want {
				t.Fatalf("page boundary length=%d", n)
			}
		}
	}
}

func TestMIPSRandomStatesAndStreamingTags(t *testing.T) {
	rng := rand.New(rand.NewSource(1310))
	var payload [264]byte
	for trial := 0; trial < 20000; trial++ {
		var key [32]byte
		rng.Read(key[:])
		rng.Read(payload[:])
		alignment, n := rng.Intn(8), rng.Intn(257)
		msg := payload[alignment : alignment+n]
		var got macState
		initialize(&key, &got)
		got.h = [3]uint64{rng.Uint64(), rng.Uint64(), uint64(trial % 6)}
		want := got
		candidate := got
		updateMIPS(&got, msg)
		updateMACCMIPS(&candidate, msg)
		updateGenericGo(&want, msg)
		if got != want || candidate != want {
			t.Fatalf("random state trial=%d length=%d alignment=%d", trial, n, alignment)
		}
	}
	for trial := 0; trial < 256; trial++ {
		var key [32]byte
		rng.Read(key[:])
		msg := make([]byte, rng.Intn(32769))
		rng.Read(msg)
		var state macState
		initialize(&key, &state)
		updateGenericGo(&state, msg)
		var want [TagSize]byte
		finalize(&want, &state.h, &state.s)
		mac := newMACGeneric(&key)
		for p := msg; len(p) > 0; {
			n := 1 + rng.Intn(129)
			if n > len(p) {
				n = len(p)
			}
			mac.Write(p[:n])
			p = p[n:]
		}
		var got, again [TagSize]byte
		mac.Sum(&got)
		mac.Sum(&again)
		if got != want || again != want {
			t.Fatalf("streaming tag trial=%d length=%d", trial, len(msg))
		}
	}
}

func TestMIPSMultiplyConcurrent(t *testing.T) {
	var workers sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		workers.Add(1)
		go func(seed int64) {
			defer workers.Done()
			rng := rand.New(rand.NewSource(seed))
			for i := 0; i < 10000; i++ {
				a, b := rng.Uint64(), rng.Uint64()
				lo, hi := mul64MIPS(a, b)
				wantHi, wantLo := bits.Mul64(a, b)
				if hi != wantHi || lo != wantLo {
					t.Errorf("concurrent multiply mismatch")
					return
				}
				if i%1024 == 0 {
					runtime.GC()
				}
			}
		}(int64(worker + 1))
	}
	workers.Wait()
}

func TestMIPSMACCConcurrent(t *testing.T) {
	var workers sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		workers.Add(1)
		go func(seed int64) {
			defer workers.Done()
			rng := rand.New(rand.NewSource(seed))
			var key [32]byte
			for trial := 0; trial < 128; trial++ {
				rng.Read(key[:])
				msg := make([]byte, 1+rng.Intn(32769))
				rng.Read(msg)
				var got macState
				initialize(&key, &got)
				want := got
				updateMACCMIPS(&got, msg)
				updateGenericGo(&want, msg)
				if got != want {
					t.Errorf("concurrent MACC mismatch: worker=%d trial=%d", seed, trial)
					return
				}
				if trial%16 == 0 {
					runtime.GC()
				}
			}
		}(int64(worker + 1))
	}
	workers.Wait()
}

func TestMIPSChainedAuthentication(t *testing.T) {
	var key [32]byte
	var input [25]byte
	for i := range key {
		key[i] = 1
	}
	for i := range input {
		input[i] = 2
	}
	// Bound the upstream 10-billion-iteration stress pattern and compare each
	// intermediate tag to the independent Go path, not only the final tag.
	for i := 0; i < 100000; i++ {
		var got, want [TagSize]byte
		Sum(&got, input[:], &key)
		var reference macState
		initialize(&key, &reference)
		updateGenericGo(&reference, input[:])
		finalize(&want, &reference.h, &reference.s)
		if got != want {
			t.Fatalf("chained tag mismatch at iteration %d: got %x, want %x", i, got, want)
		}
		copy(key[:], got[:])
		copy(key[16:], got[:])
		copy(input[:], got[:])
		copy(input[16:], got[:])
	}
}

var mipsPolySink macState

func BenchmarkMIPSPoly1305(b *testing.B) {
	for _, n := range []int{16, 64, 1200, 16384} {
		for _, implementation := range []string{"assembly", "macc", "products", "generic"} {
			b.Run(fmt.Sprintf("%d/%s", n, implementation), func(b *testing.B) {
				var key [32]byte
				rand.New(rand.NewSource(1308)).Read(key[:])
				msg := make([]byte, n)
				rand.New(rand.NewSource(1307)).Read(msg)
				var state macState
				initialize(&key, &state)
				b.SetBytes(int64(n))
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if implementation == "assembly" {
						updateMIPS(&state, msg)
					} else if implementation == "macc" {
						updateMACCMIPS(&state, msg)
					} else if implementation == "products" {
						updateProductsMIPS(&state, msg)
					} else {
						updateGenericGo(&state, msg)
					}
				}
				mipsPolySink = state
			})
		}
	}
}
