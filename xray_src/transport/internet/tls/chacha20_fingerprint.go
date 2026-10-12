package tls

import (
	"fmt"
	"slices"
	"sync"

	utls "github.com/refraction-networking/utls"
)

var chacha20Fingerprint struct {
	sync.Once
	id *utls.ClientHelloID
}

// This is an opt-in randomized fingerprint, not a modified Chrome preset.
// Select a uTLS seed whose normal ClientHello prefers ChaCha20. Keeping the
// generated hello intact also preserves REALITY's authenticated hello bytes.
func newChaCha20Fingerprint() (*utls.ClientHelloID, error) {
	weights := utls.DefaultWeights
	weights.TLSVersMax_Set_VersionTLS13 = 1
	weights.FirstKeyShare_Set_CurveP256 = 0
	weights.CipherSuites_Remove_RandomCiphers = 0
	// Modern REALITY requires the hybrid share before an optional X25519
	// share. uTLS randomizes these independently, so require both the
	// advertised hybrid group and its share instead of depending on the seed.
	weights.CurveIDs_Append_X25519 = 1
	weights.KeyShare_Append_RandomGroups = 1
	for range 128 {
		id := utls.HelloRandomizedALPN
		id.Weights = &weights
		seed, err := utls.NewPRNGSeed()
		if err != nil {
			return nil, err
		}
		id.Seed = seed
		spec, err := utls.UTLSIdToSpec(id)
		if err != nil {
			return nil, err
		}
		if spec.TLSVersMin >= utls.VersionTLS12 && len(spec.CipherSuites) > 0 &&
			spec.CipherSuites[0] == utls.TLS_CHACHA20_POLY1305_SHA256 &&
			slices.Contains(spec.CipherSuites, utls.TLS_AES_128_GCM_SHA256) &&
			slices.Contains(spec.CipherSuites, utls.TLS_AES_256_GCM_SHA384) {
			return &id, nil
		}
	}
	return nil, fmt.Errorf("could not generate a ChaCha20-first TLS fingerprint")
}
