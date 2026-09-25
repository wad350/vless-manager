package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunTunnelSpeedTestMeasuresBothDirections(t *testing.T) {
	var uploadRequests atomic.Int64
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
			if uploadRequests.Add(1) == 1 {
				http.Error(w, "retry", http.StatusServiceUnavailable)
				return
			}
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
		Client: server.Client(), DownloadParam: "bytes", DownloadUnit: 1,
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

func TestSpeedTestTrafficAccumulatesWithoutDoubleCountingProgress(t *testing.T) {
	service := newSpeedTestService(nil)
	service.recordProgress(speedTestStatus{DownloadBytes: 10, UploadBytes: 2})
	service.recordProgress(speedTestStatus{DownloadBytes: 25, UploadBytes: 7})
	service.recordProgress(speedTestStatus{DownloadBytes: 25, UploadBytes: 7})
	download, upload := service.trafficSnapshot()
	if download != 25 || upload != 7 {
		t.Fatalf("traffic snapshot = (%d, %d), want (25, 7)", download, upload)
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
