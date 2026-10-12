//go:build linux && mipsle && gc && !purego

package chacha20poly1305

import (
	"bytes"
	"encoding/binary"
	"runtime"
	"sync"
	"testing"
)

func TestMIPSAEADConcurrent(t *testing.T) {
	var workers sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		workers.Add(1)
		go func(worker byte) {
			defer workers.Done()
			var key [KeySize]byte
			key[0] = worker
			aead, err := New(key[:])
			if err != nil {
				t.Error(err)
				return
			}
			plaintext := bytes.Repeat([]byte{worker}, 16384)
			var nonce [NonceSize]byte
			var ciphertext, decoded []byte
			for i := uint64(0); i < 128; i++ {
				binary.LittleEndian.PutUint64(nonce[4:], i)
				ciphertext = aead.Seal(ciphertext[:0], nonce[:], plaintext, []byte("TLS test"))
				decoded, err = aead.Open(decoded[:0], nonce[:], ciphertext, []byte("TLS test"))
				if err != nil || !bytes.Equal(plaintext, decoded) {
					t.Errorf("worker=%d iteration=%d: decode failed: %v", worker, i, err)
					return
				}
				ciphertext[len(ciphertext)-1] ^= 1
				if _, err := aead.Open(decoded[:0], nonce[:], ciphertext, []byte("TLS test")); err == nil {
					t.Errorf("worker=%d: modified tag accepted", worker)
					return
				}
				if i%16 == 0 {
					runtime.GC()
				}
			}
		}(byte(worker + 1))
	}
	workers.Wait()
}

func BenchmarkMIPSAEADParallel(b *testing.B) {
	for _, operation := range []string{"seal", "open"} {
		b.Run(operation, func(b *testing.B) {
			b.SetBytes(16384)
			b.ReportAllocs()
			b.RunParallel(func(pb *testing.PB) {
				var key [KeySize]byte
				aead, _ := New(key[:])
				var nonce [NonceSize]byte
				plaintext := make([]byte, 16384)
				ciphertext := aead.Seal(nil, nonce[:], plaintext, nil)
				out := make([]byte, 0, 16384+Overhead)
				counter := uint64(0)
				for pb.Next() {
					if operation == "seal" {
						counter++
						binary.LittleEndian.PutUint64(nonce[4:], counter)
						out = aead.Seal(out[:0], nonce[:], plaintext, nil)
					} else {
						var err error
						out, err = aead.Open(out[:0], nonce[:], ciphertext, nil)
						if err != nil {
							b.Error(err)
							return
						}
					}
				}
				runtime.KeepAlive(out)
			})
		})
	}
}
