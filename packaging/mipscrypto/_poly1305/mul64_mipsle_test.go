//go:build linux && mipsle && gc && !purego

package poly1305

import (
	"fmt"
	"math/bits"
	"math/rand"
	"runtime"
	"sync"
	"testing"
)

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
	lengths := []int{0, 1, 15, 16, 17, 31, 32, 33, 64, 127, 1024, 16384, 16385}
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
			}
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

var mipsPolySink macState

func BenchmarkMIPSPoly1305(b *testing.B) {
	for _, n := range []int{64, 1024, 16384} {
		for _, implementation := range []string{"assembly", "generic"} {
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
					} else {
						updateGenericGo(&state, msg)
					}
				}
				mipsPolySink = state
			})
		}
	}
}
