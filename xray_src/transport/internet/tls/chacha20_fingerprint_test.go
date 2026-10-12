package tls

import (
	"bytes"
	"context"
	gotls "crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"slices"
	"testing"
	"time"

	utls "github.com/refraction-networking/utls"
	"github.com/xtls/xray-core/common/protocol/tls/cert"
)

func TestChaCha20FingerprintSpec(t *testing.T) {
	for range 30 {
		id, err := newChaCha20Fingerprint()
		if err != nil {
			t.Fatal(err)
		}
		spec, err := utls.UTLSIdToSpec(*id)
		if err != nil {
			t.Fatal(err)
		}
		if spec.TLSVersMin != utls.VersionTLS12 || spec.TLSVersMax != utls.VersionTLS13 ||
			spec.CipherSuites[0] != utls.TLS_CHACHA20_POLY1305_SHA256 ||
			!slices.Contains(spec.CipherSuites, utls.TLS_AES_128_GCM_SHA256) ||
			!slices.Contains(spec.CipherSuites, utls.TLS_AES_256_GCM_SHA384) {
			t.Fatalf("bad cipher/version policy: %+v", spec)
		}
		var curves []utls.CurveID
		var shares []utls.KeyShare
		for _, ext := range spec.Extensions {
			switch ext := ext.(type) {
			case *utls.SupportedCurvesExtension:
				curves = ext.Curves
			case *utls.KeyShareExtension:
				shares = ext.KeyShares
			}
		}
		if len(shares) < 2 || shares[0].Group != utls.X25519MLKEM768 || shares[1].Group != utls.X25519 {
			t.Fatalf("REALITY key share order is incompatible: %+v", shares)
		}
		for _, share := range shares {
			if !slices.Contains(curves, share.Group) {
				t.Fatalf("key share %v is not advertised", share.Group)
			}
		}
	}
	if GetFingerprint("chrome") != &utls.HelloChrome_Auto || GetFingerprint("") != &utls.HelloChrome_Auto {
		t.Fatal("default browser camouflage changed")
	}
	if GetFingerprint("randomized-chacha20") == nil {
		t.Fatal("profile not registered")
	}
}

func TestChaCha20FingerprintHandshake(t *testing.T) {
	ct, _ := cert.MustGenerate(nil, cert.DNSNames("localhost"))
	crtPEM, keyPEM := ct.ToPEM()
	pair, err := gotls.X509KeyPair(crtPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	root, err := x509.ParseCertificate(ct.Certificate)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name            string
		version, cipher uint16
		trust           bool
	}{
		{"TLS13 ChaCha20", gotls.VersionTLS13, gotls.TLS_CHACHA20_POLY1305_SHA256, true},
		{"TLS12 AES fallback", gotls.VersionTLS12, gotls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256, true},
		{"certificate rejection", gotls.VersionTLS13, 0, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ln, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer ln.Close()
			payload := bytes.Repeat([]byte("verified TLS payload\n"), 4096)
			serverDone := make(chan error, 1)
			go func() {
				conn, err := ln.Accept()
				if err != nil {
					serverDone <- err
					return
				}
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				cfg := &gotls.Config{Certificates: []gotls.Certificate{pair}, MinVersion: tt.version,
					MaxVersion: tt.version, NextProtos: []string{"h2"}}
				if tt.version == gotls.VersionTLS12 {
					cfg.CipherSuites = []uint16{tt.cipher}
				}
				s := gotls.Server(conn, cfg)
				err = s.Handshake()
				if err == nil {
					_, err = s.Write(payload)
				}
				serverDone <- err
			}()
			conn, err := net.DialTimeout("tcp4", ln.Addr().String(), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
			pool := x509.NewCertPool()
			if tt.trust {
				pool.AddCert(root)
			}
			c := UClient(conn, &gotls.Config{ServerName: "localhost", RootCAs: pool, NextProtos: []string{"h2"}},
				GetFingerprint("randomized-chacha20")).(*UConn)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			err = c.HandshakeContext(ctx)
			if !tt.trust {
				if err == nil {
					t.Fatal("untrusted certificate was accepted")
				}
				_ = conn.Close()
				<-serverDone
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			state := c.ConnectionState()
			if state.CipherSuite != tt.cipher || state.Version != tt.version || state.NegotiatedProtocol != "h2" {
				t.Fatalf("wrong negotiated state: %+v", state)
			}
			body := make([]byte, len(payload))
			if _, err := io.ReadFull(c, body); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(body, payload) {
				t.Fatal("TLS payload changed")
			}
			if err := <-serverDone; err != nil {
				t.Fatal(err)
			}
		})
	}
}
