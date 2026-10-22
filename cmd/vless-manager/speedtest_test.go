package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSpeedTestPreparesEveryTLSConnectionBeforeBulkTransfer(t *testing.T) {
	var bulkStarted atomic.Bool
	var handshakes atomic.Int32
	var downloads atomic.Int32
	var mu sync.Mutex
	warmed := map[string]bool{}
	releaseHandshake := make(chan struct{})
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		ready := warmed[r.RemoteAddr]
		if r.URL.Path == "/ping" {
			warmed[r.RemoteAddr] = true
		}
		mu.Unlock()
		if r.URL.Path == "/ping" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		bulkStarted.Store(true)
		if !ready || r.Header.Get("jwt") != "fixture-token" {
			http.Error(w, "unprepared worker", http.StatusConflict)
			return
		}
		downloads.Add(1)
		_, _ = io.CopyN(w, zeroReader{}, 16<<10)
	}))
	server.TLS = &tls.Config{NextProtos: []string{"http/1.1"}, GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
		if handshakes.Add(1) > 1 {
			time.Sleep(30 * time.Millisecond)
		}
		if bulkStarted.Load() {
			select {
			case <-releaseHandshake:
			case <-hello.Context().Done():
			}
		}
		return nil, nil
	}}
	server.StartTLS()
	defer func() { close(releaseHandshake); server.Close() }()
	client := server.Client()
	transport := client.Transport.(*http.Transport)
	transport.ForceAttemptHTTP2 = false
	transport.TLSHandshakeTimeout = 150 * time.Millisecond
	// Latency measurements have already left one reusable TLS connection.
	resp, err := client.Get(server.URL + "/ping")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	n, _, err := runSpeedTestPhase(ctx, speedTestConfig{
		Client: client, LatencyURL: server.URL + "/ping", PhaseLimit: 500 * time.Millisecond,
		Headers: http.Header{"Jwt": []string{"fixture-token"}},
	}, http.MethodGet, server.URL+"/download", 64<<10, 4, func(int64) {})
	if err != nil || n != 64<<10 || downloads.Load() != 4 {
		t.Fatalf("TLS readiness: bytes=%d downloads=%d err=%v", n, downloads.Load(), err)
	}
	if handshakes.Load() != 4 {
		t.Fatalf("TLS handshakes = %d, want four reserved connections", handshakes.Load())
	}
}

func TestSpeedTestReusesPreparedConnectionsAcrossDirections(t *testing.T) {
	var handshakes atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			if r.ContentLength > 1<<20 {
				t.Errorf("upload chunk = %d", r.ContentLength)
			}
			_, _ = io.Copy(io.Discard, r.Body)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		size, _ := strconv.ParseInt(r.URL.Query().Get("bytes"), 10, 64)
		if size > 1<<20 {
			t.Errorf("download chunk = %d", size)
		}
		w.Header().Set("Content-Length", fmt.Sprint(size))
		_, _ = io.CopyN(w, zeroReader{}, size)
	}))
	server.TLS = &tls.Config{GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) { handshakes.Add(1); return nil, nil }}
	server.StartTLS()
	defer server.Close()
	client := server.Client()
	defer client.CloseIdleConnections()
	result, err := runTunnelSpeedTest(context.Background(), speedTestConfig{
		Client: client, LatencyURL: server.URL, DownloadURL: server.URL, UploadURL: server.URL,
		LatencyParam: "bytes", DownloadParam: "bytes", DownloadSize: 8 << 20, UploadSize: 8 << 20, Workers: 4,
	}, func(speedTestStatus) {})
	if err != nil || result.DownloadBytes != 8<<20 || result.UploadBytes != 8<<20 || handshakes.Load() != 4 {
		t.Fatalf("result=%+v handshakes=%d err=%v", result, handshakes.Load(), err)
	}
}

func TestSpeedTestServerSelectionKeepsFastestConnection(t *testing.T) {
	var fastestConnections atomic.Int32
	var servers []*httptest.Server
	var candidates []speedTestRUServer
	for i := 0; i < 6; i++ {
		server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if i > 0 {
				time.Sleep(50 * time.Millisecond)
			}
			w.WriteHeader(http.StatusNoContent)
		}))
		if i == 0 {
			server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
				if state == http.StateNew {
					fastestConnections.Add(1)
				}
			}
		}
		server.StartTLS()
		servers = append(servers, server)
		defer server.Close()
		candidates = append(candidates, speedTestRUServer{BaseURL: server.URL})
	}
	client := newSpeedTestHTTPClient(4)
	client.Transport.(*http.Transport).TLSClientConfig = servers[0].Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	defer client.CloseIdleConnections()
	best, err := fastestSpeedTestRUServer(context.Background(), client, candidates, nil)
	if err != nil || best.BaseURL != servers[0].URL {
		t.Fatalf("best=%+v err=%v", best, err)
	}
	resp, err := client.Get(best.BaseURL + "/ping.php")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if fastestConnections.Load() != 1 {
		t.Fatalf("fastest connection was evicted: %d handshakes", fastestConnections.Load())
	}
}

func TestSpeedTestDrainsLastBoundedResponse(t *testing.T) {
	var downloads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		size, _ := strconv.ParseInt(r.URL.Query().Get("bytes"), 10, 64)
		if size > 0 {
			downloads.Add(1)
			time.Sleep(80 * time.Millisecond)
		}
		w.Header().Set("Content-Length", fmt.Sprint(size))
		_, _ = io.CopyN(w, zeroReader{}, size)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	n, elapsed, err := runSpeedTestPhase(ctx, speedTestConfig{Client: server.Client(), DownloadParam: "bytes", PhaseLimit: 30 * time.Millisecond},
		http.MethodGet, server.URL, 2<<20, 1, func(int64) {})
	if err != nil || n != 1<<20 || downloads.Load() != 1 || elapsed < 80*time.Millisecond {
		t.Fatalf("bytes=%d requests=%d elapsed=%v err=%v", n, downloads.Load(), elapsed, err)
	}
}

func TestSpeedTestPreparationHonorsCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	n, _, err := runSpeedTestPhase(ctx, speedTestConfig{Client: server.Client(), LatencyURL: server.URL},
		http.MethodGet, server.URL, 1024, 4, func(int64) {})
	if !errors.Is(err, context.DeadlineExceeded) || n != 0 {
		t.Fatalf("preparation cancellation: bytes=%d err=%v", n, err)
	}
}

type interruptedSpeedTestBody struct {
	ctx       context.Context
	remaining int
	wait      bool
}

func (b *interruptedSpeedTestBody) Read(p []byte) (int, error) {
	if b.remaining > 0 {
		n := min(len(p), b.remaining)
		b.remaining -= n
		return n, nil
	}
	if b.wait {
		<-b.ctx.Done()
	}
	return 0, io.ErrUnexpectedEOF
}

func (b *interruptedSpeedTestBody) Close() error { return nil }

func TestSpeedTestPhaseDistinguishesDeadlineEOF(t *testing.T) {
	for _, wait := range []bool{false, true} {
		t.Run(fmt.Sprint(wait), func(t *testing.T) {
			client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Body: &interruptedSpeedTestBody{
					ctx: r.Context(), remaining: 1 << 20, wait: wait,
				}}, nil
			})}
			n, _, err := runSpeedTestPhase(context.Background(), speedTestConfig{Client: client, PhaseLimit: 50 * time.Millisecond},
				http.MethodGet, "https://fixture.example/download", 2<<20, 1, func(int64) {})
			if n != 1<<20 || (wait && err != nil) || (!wait && !errors.Is(err, io.ErrUnexpectedEOF)) {
				t.Fatalf("wait=%v bytes=%d err=%v", wait, n, err)
			}
		})
	}
}

func TestSpeedTestDownloadRetriesOnlyBeforePayload(t *testing.T) {
	for _, kind := range []string{"recover", "persistent", "partial", "cancel"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var requests atomic.Int32
			client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				attempt := requests.Add(1)
				if req.Method != http.MethodGet || req.URL.Query().Get("bytes") != "1024" {
					t.Fatalf("request changed: %s %s", req.Method, req.URL)
				}
				if kind == "cancel" {
					cancel()
					return nil, context.Canceled
				}
				if kind == "persistent" || kind == "recover" && attempt == 1 {
					return nil, &net.DNSError{Err: "fixture timeout", IsTimeout: true}
				}
				var body io.ReadCloser = io.NopCloser(strings.NewReader(strings.Repeat("x", 1024)))
				if kind == "partial" {
					body = &interruptedSpeedTestBody{ctx: ctx, remaining: 128}
				}
				return &http.Response{StatusCode: http.StatusOK, Body: body, Header: make(http.Header)}, nil
			})}
			var bytes atomic.Int64
			err := transferSpeedTestRequest(ctx, speedTestConfig{Client: client, DownloadParam: "bytes", DownloadUnit: 1},
				http.MethodGet, "https://fixture.test/download", 1024, &bytes)
			switch kind {
			case "recover":
				if err != nil || bytes.Load() != 1024 || requests.Load() != 2 {
					t.Fatalf("bytes=%d requests=%d err=%v", bytes.Load(), requests.Load(), err)
				}
			case "persistent":
				if err == nil || bytes.Load() != 0 || requests.Load() != 2 {
					t.Fatalf("bytes=%d requests=%d err=%v", bytes.Load(), requests.Load(), err)
				}
			case "partial":
				if !errors.Is(err, io.ErrUnexpectedEOF) || bytes.Load() != 128 || requests.Load() != 1 {
					t.Fatalf("bytes=%d requests=%d err=%v", bytes.Load(), requests.Load(), err)
				}
			case "cancel":
				if !errors.Is(err, context.Canceled) || bytes.Load() != 0 || requests.Load() != 1 {
					t.Fatalf("bytes=%d requests=%d err=%v", bytes.Load(), requests.Load(), err)
				}
			}
		})
	}
}

func TestSpeedTestUploadChunksWithoutSizeParameter(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength > 1<<20 || r.URL.RawQuery != "" {
			http.Error(w, "invalid upload chunk", http.StatusBadRequest)
			return
		}
		requests.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	n, _, err := runSpeedTestPhase(context.Background(), speedTestConfig{Client: server.Client()},
		http.MethodPost, server.URL, 5<<20, 2, func(int64) {})
	if err != nil || n != 5<<20 || requests.Load() != 6 {
		t.Fatalf("bytes=%d requests=%d err=%v", n, requests.Load(), err)
	}
}

func TestRunTunnelSpeedTestMeasuresBothDirections(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/down":
			size, err := strconv.ParseInt(r.URL.Query().Get("bytes"), 10, 64)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Length", fmt.Sprint(size))
			_, _ = io.CopyN(w, zeroReader{}, size)
		case "/up":
			if r.URL.Query().Get("bytes") != fmt.Sprint(r.ContentLength) {
				http.Error(w, "missing upload size", http.StatusBadRequest)
				return
			}
			if r.ContentLength > 1<<20 {
				http.Error(w, "upload chunk too large", http.StatusRequestEntityTooLarge)
				return
			}
			_, _ = io.Copy(io.Discard, r.Body)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	var last speedTestStatus
	result, err := runTunnelSpeedTest(context.Background(), speedTestConfig{
		LatencyURL:    server.URL + "/down",
		DownloadURL:   server.URL + "/down",
		UploadURL:     server.URL + "/up",
		DownloadSize:  2 << 20,
		UploadSize:    1 << 20,
		Workers:       2,
		Client:        server.Client(),
		LatencyParam:  "bytes",
		DownloadParam: "bytes",
		UploadParam:   "bytes",
		DownloadUnit:  1,
		UploadUnit:    1,
	}, func(progress speedTestStatus) { last = progress })
	if err != nil {
		t.Fatal(err)
	}
	if result.DownloadBytes != 2<<20 || result.UploadBytes != 1<<20 {
		t.Fatalf("transferred download=%d upload=%d", result.DownloadBytes, result.UploadBytes)
	}
	if result.DownloadMbps <= 0 || result.UploadMbps <= 0 || result.LatencyMS <= 0 {
		t.Fatalf("invalid result: %+v", result)
	}
	if last.Phase != "upload" || last.Progress != 100 {
		t.Fatalf("last progress = %+v", last)
	}
}

func TestSpeedTestUploadCountsRetriesAndReportsBeforeResponse(t *testing.T) {
	var requests atomic.Int32
	var transferred atomic.Int64
	var acknowledged atomic.Int64
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		n, err := io.Copy(io.Discard, req.Body)
		if err != nil {
			return nil, err
		}
		attempt := requests.Add(1)
		if transferred.Load() != int64(attempt)*(1<<20) || n != 1<<20 {
			t.Fatalf("live upload bytes=%d request=%d", transferred.Load(), n)
		}
		code := http.StatusNoContent
		if attempt == 1 {
			code = http.StatusServiceUnavailable
		}
		return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
	})}
	err := transferSpeedTestWorker(context.Background(), speedTestConfig{Client: client, uploadAck: &acknowledged},
		http.MethodPost, "https://fixture.test/upload", 1<<20, &transferred)
	if err != nil || requests.Load() != 2 || transferred.Load() != 2<<20 || acknowledged.Load() != 1<<20 {
		t.Fatalf("requests=%d bytes=%d ack=%d err=%v", requests.Load(), transferred.Load(), acknowledged.Load(), err)
	}
}

func TestSpeedTestUploadDeadlineRequiresAnAcknowledgedRequest(t *testing.T) {
	for _, acknowledge := range []bool{false, true} {
		t.Run(fmt.Sprint(acknowledge), func(t *testing.T) {
			var requests atomic.Int32
			client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				_, _ = io.Copy(io.Discard, req.Body)
				if requests.Add(1) == 1 && acknowledge {
					return &http.Response{StatusCode: http.StatusNoContent, Body: http.NoBody, Header: make(http.Header)}, nil
				}
				<-req.Context().Done()
				return nil, req.Context().Err()
			})}
			n, _, err := runSpeedTestPhase(context.Background(), speedTestConfig{Client: client, PhaseLimit: 30 * time.Millisecond},
				http.MethodPost, "https://fixture.test/upload", 4<<20, 1, func(int64) {})
			if n < 1<<20 || (acknowledge && err != nil) || (!acknowledge && !errors.Is(err, context.DeadlineExceeded)) {
				t.Fatalf("bytes=%d ack=%v err=%v", n, acknowledge, err)
			}
		})
	}
}

func TestSpeedTestServerFallbackOnlyBeforePayload(t *testing.T) {
	for _, provider := range []string{speedTestProviderCloudflare, speedTestProviderSpeedtestRU} {
		for _, scenario := range []string{"prepare", "progress", "result", "cancelled", "certificate", "stopped"} {
			t.Run(provider+"/"+scenario, func(t *testing.T) {
				api := newHandlerTestAPI(t)
				api.pm.running = scenario != "stopped"
				client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					body := ""
					switch req.URL.Path {
					case "/api/nearest_servers":
						body = `{"data":[{"src":"https://server-a.test","port":443},{"src":"https://server-b.test","port":443}]}`
					case "/api/server/gentoken":
						body = `{"token":"fixture-token"}`
					case "/ping.php":
						if req.URL.Host == "server-b.test:443" {
							time.Sleep(5 * time.Millisecond)
						}
					default:
						t.Fatalf("unexpected URL: %s", req.URL)
					}
					return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
				})}
				service := newSpeedTestService(api)
				attempts := 0
				service.run = func(ctx context.Context, cfg speedTestConfig, progress func(speedTestStatus)) (speedTestResult, error) {
					attempts++
					if attempts == 2 {
						if provider == speedTestProviderSpeedtestRU && !strings.Contains(cfg.LatencyURL, "server-b.test") {
							t.Fatalf("failed server was selected again: %s", cfg.LatencyURL)
						}
						if provider == speedTestProviderCloudflare && cfg.LatencyURL != defaultSpeedTestDownloadURL {
							t.Fatalf("Cloudflare endpoint changed: %s", cfg.LatencyURL)
						}
						return speedTestResult{DownloadBytes: 10, UploadBytes: 10}, nil
					}
					switch scenario {
					case "progress":
						progress(speedTestStatus{DownloadBytes: 1})
					case "result":
						return speedTestResult{DownloadBytes: 1}, io.ErrUnexpectedEOF
					case "cancelled":
						return speedTestResult{}, context.Canceled
					case "certificate":
						return speedTestResult{}, errors.New("certificate validation failed")
					}
					return speedTestResult{}, fmt.Errorf("preparation: %w", io.ErrUnexpectedEOF)
				}
				_, err := service.runWithServerSelection(context.Background(), provider, client, func(operationProgress) {})
				if scenario == "prepare" {
					if err != nil || attempts != 2 {
						t.Fatalf("attempts=%d err=%v", attempts, err)
					}
				} else if err == nil || attempts != 1 {
					t.Fatalf("retried a non-recoverable sample: attempts=%d err=%v", attempts, err)
				}
			})
		}
	}
}

func TestSpeedTestPreparationRetriesAreBounded(t *testing.T) {
	api := newHandlerTestAPI(t)
	api.pm.running = true
	service := newSpeedTestService(api)
	attempts := 0
	service.run = func(context.Context, speedTestConfig, func(speedTestStatus)) (speedTestResult, error) {
		attempts++
		return speedTestResult{}, io.EOF
	}
	_, err := service.runWithServerSelection(context.Background(), speedTestProviderCloudflare,
		&http.Client{}, func(operationProgress) {})
	if err == nil || attempts != 3 {
		t.Fatalf("attempts=%d err=%v", attempts, err)
	}
}

func TestRunTunnelSpeedTestHonorsCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("bytes") == "0" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		<-r.Context().Done()
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := runTunnelSpeedTest(ctx, speedTestConfig{
		LatencyURL:    server.URL,
		DownloadURL:   server.URL,
		UploadURL:     server.URL,
		DownloadSize:  1 << 20,
		UploadSize:    1 << 20,
		Workers:       1,
		Client:        server.Client(),
		LatencyParam:  "bytes",
		DownloadParam: "bytes",
		UploadParam:   "bytes",
		DownloadUnit:  1,
		UploadUnit:    1,
	}, func(speedTestStatus) {})
	if err == nil {
		t.Fatal("cancelled speed test returned no error")
	}
}

func TestSpeedTestPhaseUsesPartialSampleAtDeadline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/partial" {
			_, _ = io.CopyN(w, zeroReader{}, 2<<20)
			w.(http.Flusher).Flush()
		}
		<-r.Context().Done()
	}))
	defer server.Close()
	config := speedTestConfig{
		Client:     server.Client(),
		PhaseLimit: 200 * time.Millisecond,
	}
	bytes, _, err := runSpeedTestPhase(context.Background(), config, http.MethodGet, server.URL+"/partial", 4<<20, 1, func(int64) {})
	if err != nil || bytes < 1<<20 || bytes >= 4<<20 {
		t.Fatalf("partial sample: bytes=%d, err=%v", bytes, err)
	}
	bytes, _, err = runSpeedTestPhase(context.Background(), config, http.MethodGet, server.URL+"/no-data", 4<<20, 1, func(int64) {})
	if !errors.Is(err, context.DeadlineExceeded) || bytes != 0 {
		t.Fatalf("empty sample: bytes=%d, err=%v", bytes, err)
	}
}

func TestSpeedTestRejectsStartWithoutVPN(t *testing.T) {
	api := newHandlerTestAPI(t)
	rec := apiRequest(t, api, http.MethodPost, "/api/speedtest", "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestSpeedTestHelpers(t *testing.T) {
	client := newSpeedTestHTTPClient(speedTestWorkers)
	defer client.CloseIdleConnections()
	transport := client.Transport.(*http.Transport)
	if transport.TLSHandshakeTimeout != 8*time.Second || transport.IdleConnTimeout != 0 || transport.MaxIdleConns < 7 {
		t.Fatalf("unexpected connection policy: handshake=%v idle=%v pool=%d", transport.TLSHandshakeTimeout, transport.IdleConnTimeout, transport.MaxIdleConns)
	}
	if got := speedTestDownloadURL("https://example.test/down?token=x", 42); got != "https://example.test/down?bytes=42&token=x" {
		t.Fatalf("download URL = %q", got)
	}
	if got := trafficDelta(50, 20); got != 30 {
		t.Fatalf("traffic delta = %d", got)
	}
	if got := trafficDelta(20, 50); got != 0 {
		t.Fatalf("wrapped traffic delta = %d", got)
	}
	if got := speedTestRequestURL("https://example.test/download.php", "ckSize", 2_500_000, 1_000_000, false); got != "https://example.test/download.php?ckSize=3" {
		t.Fatalf("Speedtest.ru URL = %q", got)
	}
	if !validSpeedTestProvider(speedTestProviderCloudflare) || !validSpeedTestProvider(speedTestProviderSpeedtestRU) || validSpeedTestProvider("unknown") {
		t.Fatal("speed test provider validation is incorrect")
	}
	if got := addTrafficCounters(100, 40); got != 140 {
		t.Fatalf("combined traffic = %d", got)
	}
	if got := addTrafficCounters(^uint64(0)-5, 10); got != ^uint64(0) {
		t.Fatalf("overflowing combined traffic = %d", got)
	}
}

func TestSpeedTestProgressRetainsLatestSnapshot(t *testing.T) {
	service := newSpeedTestService(nil)
	service.recordProgress(speedTestStatus{DownloadBytes: 10, UploadBytes: 2})
	service.recordProgress(speedTestStatus{DownloadBytes: 25, UploadBytes: 7})
	service.recordProgress(speedTestStatus{DownloadBytes: 25, UploadBytes: 7})
	status := service.snapshot()
	if status.DownloadBytes != 25 || status.UploadBytes != 7 {
		t.Fatalf("progress snapshot = %+v, want download=25 upload=7", status)
	}
}

func TestPrepareSpeedTestRUConfig(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/nearest_servers":
			if r.Header.Get("x-api-key") != speedTestRUAPIKey {
				http.Error(w, "missing API key", http.StatusUnauthorized)
				return
			}
			port, err := strconv.Atoi(server.URL[strings.LastIndex(server.URL, ":")+1:])
			if err != nil {
				t.Fatal(err)
			}
			fmt.Fprintf(w, `{"data":[{"id":1,"src":%q,"port":%d,"name":"test","city":"Москва","source":"Test ISP"}]}`, server.URL, port)
		case "/api/server/gentoken":
			if r.Method != http.MethodPost || r.Header.Get("x-api-key") != speedTestRUAPIKey {
				http.Error(w, "bad token request", http.StatusUnauthorized)
				return
			}
			_, _ = io.WriteString(w, `{"token":"test-jwt"}`)
		case "/ping.php":
			if r.Header.Get("jwt") != "test-jwt" {
				http.Error(w, "missing JWT", http.StatusUnauthorized)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	config, err := prepareSpeedTestRUConfig(context.Background(), server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if config.Server != "Speedtest.ru · Test ISP · Москва" || config.Headers.Get("jwt") != "test-jwt" {
		t.Fatalf("unexpected config: server=%q headers=%v", config.Server, config.Headers)
	}
	if config.DownloadParam != "ckSize" || config.DownloadUnit != 1_000_000 || config.LatencyURL == "" {
		t.Fatalf("unexpected endpoint config: %+v", config)
	}
}
