package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func stalledSpeedTestTLSConn(closed chan<- struct{}) net.Conn {
	client, peer := net.Pipe()
	go func() {
		defer peer.Close()
		io.Copy(io.Discard, peer)
		closed <- struct{}{}
	}()
	return client
}

func TestSpeedTestTLSReplacesSilentStreamBeforeHTTP(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	address := strings.TrimPrefix(server.URL, "https://")
	closed := make(chan struct{}, 1)
	var attempts atomic.Int32
	dial := func(ctx context.Context, network, address string) (net.Conn, error) {
		if attempts.Add(1) == 1 {
			return stalledSpeedTestTLSConn(closed), nil
		}
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
	config := server.Client().Transport.(*http.Transport).TLSClientConfig
	started := time.Now()
	conn, err := dialSpeedTestTLS(context.Background(), "tcp", address, dial, config, time.Second, 20*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if attempts.Load() != 2 || requests.Load() != 0 || time.Since(started) > 500*time.Millisecond {
		t.Fatalf("attempts=%d HTTP requests=%d elapsed=%v", attempts.Load(), requests.Load(), time.Since(started))
	}
	if config.ServerName != "" {
		t.Fatalf("config modified=%q", config.ServerName)
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("silent stream was not closed")
	}
}

func TestSpeedTestTLSFastConnectionReusedWithoutSpare(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	var attempts atomic.Int32
	dial := func(ctx context.Context, network, address string) (net.Conn, error) {
		attempts.Add(1)
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
	tr := server.Client().Transport.(*http.Transport).Clone()
	tr.ForceAttemptHTTP2 = false
	tr.DialTLSContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		return dialSpeedTestTLS(ctx, network, address, dial, tr.TLSClientConfig, time.Second, 500*time.Millisecond)
	}
	client := &http.Client{Transport: tr}
	defer client.CloseIdleConnections()
	var traceStarts, traceEnds atomic.Int32
	ctx := httptrace.WithClientTrace(context.Background(), &httptrace.ClientTrace{
		TLSHandshakeStart: func() { traceStarts.Add(1) },
		TLSHandshakeDone: func(state tls.ConnectionState, err error) {
			traceEnds.Add(1)
			if err != nil || !state.HandshakeComplete {
				t.Errorf("TLS trace state=%+v err=%v", state, err)
			}
		},
	})
	for i := 0; i < 3; i++ {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	if attempts.Load() != 1 || traceStarts.Load() != 1 || traceEnds.Load() != 1 {
		t.Fatalf("dials=%d trace=%d/%d", attempts.Load(), traceStarts.Load(), traceEnds.Load())
	}
}

func TestSpeedTestTLSSharedDeadlineAndCancellation(t *testing.T) {
	for _, cancelEarly := range []bool{false, true} {
		t.Run(map[bool]string{false: "deadline", true: "cancel"}[cancelEarly], func(t *testing.T) {
			closed := make(chan struct{}, 2)
			var attempts atomic.Int32
			dial := func(context.Context, string, string) (net.Conn, error) {
				attempts.Add(1)
				return stalledSpeedTestTLSConn(closed), nil
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if cancelEarly {
				time.AfterFunc(50*time.Millisecond, cancel)
			}
			started := time.Now()
			conn, err := dialSpeedTestTLS(ctx, "tcp", "example.test:443", dial, nil, 100*time.Millisecond, 10*time.Millisecond)
			want := context.DeadlineExceeded
			if cancelEarly {
				want = context.Canceled
			}
			if conn != nil || !errors.Is(err, want) || attempts.Load() != 2 || time.Since(started) > 500*time.Millisecond {
				t.Fatalf("conn=%v err=%v attempts=%d elapsed=%v", conn, err, attempts.Load(), time.Since(started))
			}
			for i := 0; i < 2; i++ {
				select {
				case <-closed:
				case <-time.After(time.Second):
					t.Fatal("cancelled stream leaked")
				}
			}
		})
	}
}

func TestSpeedTestTLSRejectsInvalidCertificateWithoutRetry(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	var attempts atomic.Int32
	dial := func(ctx context.Context, network, address string) (net.Conn, error) {
		attempts.Add(1)
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
	conn, err := dialSpeedTestTLS(context.Background(), "tcp", strings.TrimPrefix(server.URL, "https://"),
		dial, nil, time.Second, 500*time.Millisecond)
	var certErr *tls.CertificateVerificationError
	if conn != nil || !errors.As(err, &certErr) || attempts.Load() != 1 {
		t.Fatalf("conn=%v err=%v attempts=%d", conn, err, attempts.Load())
	}
	var authorityErr x509.UnknownAuthorityError
	if !errors.As(err, &authorityErr) {
		t.Fatalf("certificate validation was changed: %v", err)
	}
}
