//go:build !race

package buf

import (
	"errors"
	"runtime"
	"runtime/debug"
	"sync"
	"testing"
)

type auditFailingReader struct {
	payload []byte
	err     error
}

func (r *auditFailingReader) Read(dst []byte) (int, error) {
	n := copy(dst, r.payload)
	r.payload = r.payload[n:]
	if len(r.payload) == 0 {
		return n, r.err
	}
	return n, nil
}

func TestReadAllToBytesReleasesBuffersOnError(t *testing.T) {
	for _, size := range []int{0, 17, Size + 17} {
		t.Run(stringSize(size), func(t *testing.T) {
			// The race runtime randomly drops sync.Pool.Put values, so this
			// allocation probe is run without -race. A single P and disabled
			// GC make ordinary sync.Pool reuse deterministic.
			// These tests deliberately do not run in parallel.
			previousProcs := runtime.GOMAXPROCS(1)
			previousGC := debug.SetGCPercent(-1)
			previousPool := pool
			t.Cleanup(func() {
				pool = previousPool
				debug.SetGCPercent(previousGC)
				runtime.GOMAXPROCS(previousProcs)
			})

			allocations := 0
			pool = &sync.Pool{New: func() any {
				allocations++
				return make([]byte, Size)
			}}
			sentinel := errors.New("injected read failure")
			result, err := ReadAllToBytes(&auditFailingReader{payload: make([]byte, size), err: sentinel})
			if result != nil || !errors.Is(err, sentinel) {
				t.Fatalf("unexpected result: %v, %v", result, err)
			}

			allocatedDuringRead := allocations
			var borrowed []*Buffer
			for range allocatedDuringRead {
				borrowed = append(borrowed, New())
			}
			defer ReleaseMulti(MultiBuffer(borrowed))
			if allocations != allocatedDuringRead {
				t.Fatalf("read retained buffers: %d allocations before reuse, %d after", allocatedDuringRead, allocations)
			}
		})
	}
}

func stringSize(size int) string {
	switch size {
	case 0:
		return "empty"
	case 17:
		return "partial buffer"
	default:
		return "full and partial buffers"
	}
}
