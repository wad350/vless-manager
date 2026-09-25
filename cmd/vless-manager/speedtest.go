package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

const (
	speedTestProviderCloudflare  = "cloudflare"
	speedTestProviderSpeedtestRU = "speedtest-ru"
	speedTestRUAPIBase           = "https://speedtest.ru"
	speedTestRUAPIKey            = "5f3287b55fbcd8076919114885f8f3f7"
	defaultSpeedTestDownloadURL  = "https://speed.cloudflare.com/__down"
	defaultSpeedTestUploadURL    = "https://speed.cloudflare.com/__up"
	speedTestDownloadBytes       = int64(100 << 20)
	speedTestUploadBytes         = int64(40 << 20)
	speedTestWorkers             = 4
)

type speedTestStatus struct {
	State          string     `json:"state"`
	Phase          string     `json:"phase,omitempty"`
	Message        string     `json:"message,omitempty"`
	Progress       int        `json:"progress"`
	LatencyMS      float64    `json:"latency_ms,omitempty"`
	JitterMS       float64    `json:"jitter_ms,omitempty"`
	DownloadMbps   float64    `json:"download_mbps,omitempty"`
	UploadMbps     float64    `json:"upload_mbps,omitempty"`
	DownloadBytes  int64      `json:"download_bytes,omitempty"`
	UploadBytes    int64      `json:"upload_bytes,omitempty"`
	TunnelBytes    uint64     `json:"tunnel_bytes,omitempty"`
	TunnelVerified bool       `json:"tunnel_verified,omitempty"`
	Server         string     `json:"server,omitempty"`
	Provider       string     `json:"provider,omitempty"`
	Error          string     `json:"error,omitempty"`
	StartedAt      *time.Time `json:"started_at,omitempty"`
	CompletedAt    *time.Time `json:"completed_at,omitempty"`
}

type speedTestResult struct {
	LatencyMS     float64
	JitterMS      float64
	DownloadMbps  float64
	UploadMbps    float64
	DownloadBytes int64
	UploadBytes   int64
}

type speedTestConfig struct {
	Server        string
	LatencyURL    string
	DownloadURL   string
	UploadURL     string
	DownloadSize  int64
	UploadSize    int64
	Workers       int
	Client        *http.Client
	Headers       http.Header
	LatencyParam  string
	DownloadParam string
	UploadParam   string
	DownloadUnit  int64
	UploadUnit    int64
	CacheBust     bool
	PhaseLimit    time.Duration
}

type speedTestStartRequest struct {
	Provider string `json:"provider"`
}

type speedTestService struct {
	mu              sync.Mutex
	api             *apiServer
	status          speedTestStatus
	trafficDownload uint64
	trafficUpload   uint64
	run             func(context.Context, speedTestConfig, func(speedTestStatus)) (speedTestResult, error)
}

func newSpeedTestService(api *apiServer) *speedTestService {
	return &speedTestService{
		api:    api,
		status: speedTestStatus{State: "idle", Message: "Тест ещё не запускался"},
		run:    runTunnelSpeedTest,
	}
}

func (s *speedTestService) snapshot() speedTestStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

func (s *speedTestService) update(fn func(*speedTestStatus)) speedTestStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(&s.status)
	return s.status
}

func (s *speedTestService) recordProgress(progress speedTestStatus) speedTestStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	if progress.DownloadBytes > s.status.DownloadBytes {
		s.trafficDownload += uint64(progress.DownloadBytes - s.status.DownloadBytes)
	}
	if progress.UploadBytes > s.status.UploadBytes {
		s.trafficUpload += uint64(progress.UploadBytes - s.status.UploadBytes)
	}
	s.status.State = "running"
	s.status.Phase = progress.Phase
	s.status.Message = progress.Message
	s.status.Progress = progress.Progress
	s.status.LatencyMS = progress.LatencyMS
	s.status.JitterMS = progress.JitterMS
	s.status.DownloadMbps = progress.DownloadMbps
	s.status.UploadMbps = progress.UploadMbps
	s.status.DownloadBytes = progress.DownloadBytes
	s.status.UploadBytes = progress.UploadBytes
	return s.status
}

func (s *speedTestService) trafficSnapshot() (download, upload uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.trafficDownload, s.trafficUpload
}

func (s *apiServer) handleSpeedTest(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, s.speedTest.snapshot())
	case http.MethodPost:
		if !s.pm.TunRunning() {
			writeError(w, http.StatusConflict, "сначала включите VPN")
			return
		}
		request := speedTestStartRequest{Provider: speedTestProviderCloudflare}
		if r.Body != nil && r.ContentLength != 0 {
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&request); err != nil {
				writeError(w, http.StatusBadRequest, "некорректные параметры теста")
				return
			}
		}
		if !validSpeedTestProvider(request.Provider) {
			writeError(w, http.StatusBadRequest, "неизвестный сервис тестирования")
			return
		}
		if !s.speedTest.start(request.Provider) {
			writeError(w, http.StatusConflict, "тест скорости уже выполняется")
			return
		}
		w.WriteHeader(http.StatusAccepted)
		writeJSON(w, s.speedTest.snapshot())
	case http.MethodDelete:
		if !s.operations.CancelKind("speedtest") {
			writeError(w, http.StatusConflict, "активного теста скорости нет")
			return
		}
		s.speedTest.update(func(status *speedTestStatus) {
			status.State = "cancelling"
			status.Message = "Останавливаем тест"
		})
		writeJSON(w, s.speedTest.snapshot())
	default:
		writeError(w, http.StatusMethodNotAllowed, "GET, POST or DELETE only")
	}
}

func (s *speedTestService) start(provider string) bool {
	s.mu.Lock()
	if speedTestBusy(s.status.State) {
		s.mu.Unlock()
		return false
	}
	now := time.Now()
	s.status = speedTestStatus{
		State:     "queued",
		Message:   "Ожидает запуска",
		Server:    speedTestProviderName(provider),
		Provider:  provider,
		StartedAt: &now,
	}
	s.mu.Unlock()

	go s.execute(provider)
	return true
}

func speedTestBusy(state string) bool {
	return state == "queued" || state == "running" || state == "cancelling"
}

func (s *speedTestService) execute(provider string) {
	err := s.api.operations.Run(context.Background(), operationRequest{
		Kind:        "speedtest",
		Title:       "Тест скорости туннеля",
		Source:      "manual",
		Cancellable: true,
		StallLimit:  30 * time.Second,
	}, func(ctx context.Context, report func(operationProgress)) error {
		before, running := s.api.pm.TrafficSnapshot()
		if !running {
			return errors.New("VPN выключен")
		}
		s.update(func(status *speedTestStatus) {
			status.State = "running"
			status.Message = "Выбираем сервер"
		})
		config, prepareErr := prepareSpeedTestConfig(ctx, provider)
		if prepareErr != nil {
			return prepareErr
		}
		s.update(func(status *speedTestStatus) {
			status.Server = config.Server
			status.Message = "Проверяем задержку"
		})
		result, runErr := s.run(ctx, config, func(progress speedTestStatus) {
			s.recordProgress(progress)
			report(operationProgress{Done: progress.Progress, Total: 100, Message: progress.Message})
		})
		if runErr != nil {
			return runErr
		}

		after, stillRunning := s.api.pm.TrafficSnapshot()
		tunnelBytes := trafficDelta(after.VPNDownload, before.VPNDownload) + trafficDelta(after.VPNUpload, before.VPNUpload)
		measuredBytes := uint64(max(int64(0), result.DownloadBytes+result.UploadBytes))
		verified := stillRunning && measuredBytes > 0 && tunnelBytes >= measuredBytes/2
		completed := time.Now()
		s.update(func(status *speedTestStatus) {
			status.State = "complete"
			status.Phase = "complete"
			status.Message = "Тест завершён"
			status.Progress = 100
			status.LatencyMS = result.LatencyMS
			status.JitterMS = result.JitterMS
			status.DownloadMbps = result.DownloadMbps
			status.UploadMbps = result.UploadMbps
			status.DownloadBytes = result.DownloadBytes
			status.UploadBytes = result.UploadBytes
			status.TunnelBytes = tunnelBytes
			status.TunnelVerified = verified
			status.CompletedAt = &completed
		})
		s.api.pm.event(serviceLogInfo, "speedtest", "test.completed", "тест скорости туннеля завершён",
			field("latency_ms", fmt.Sprintf("%.1f", result.LatencyMS)),
			field("download_mbps", fmt.Sprintf("%.2f", result.DownloadMbps)),
			field("upload_mbps", fmt.Sprintf("%.2f", result.UploadMbps)),
			field("tunnel_verified", verified), field("tunnel_bytes", tunnelBytes))
		return nil
	})
	if err != nil {
		s.fail(err)
	}
}

func validSpeedTestProvider(provider string) bool {
	return provider == speedTestProviderCloudflare || provider == speedTestProviderSpeedtestRU
}

func speedTestProviderName(provider string) string {
	if provider == speedTestProviderSpeedtestRU {
		return "Speedtest.ru"
	}
	return "Cloudflare"
}

func (s *speedTestService) fail(err error) {
	completed := time.Now()
	state := "error"
	message := "Тест не завершён"
	if errors.Is(err, errOperationCancelled) || errors.Is(err, context.Canceled) {
		state = "cancelled"
		message = "Тест остановлен"
	}
	s.update(func(status *speedTestStatus) {
		status.State = state
		status.Message = message
		status.Error = err.Error()
		status.CompletedAt = &completed
	})
}

func defaultSpeedTestConfig() speedTestConfig {
	return cloudflareSpeedTestConfig(newSpeedTestHTTPClient(speedTestWorkers))
}

func newSpeedTestHTTPClient(workers int) *http.Client {
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 8 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConns:          workers,
		MaxIdleConnsPerHost:   workers,
		MaxConnsPerHost:       workers,
		IdleConnTimeout:       15 * time.Second,
		TLSHandshakeTimeout:   8 * time.Second,
		ResponseHeaderTimeout: 8 * time.Second,
		DisableCompression:    true,
	}
	return &http.Client{Transport: transport}
}

func cloudflareSpeedTestConfig(client *http.Client) speedTestConfig {
	return speedTestConfig{
		Server:        "Cloudflare",
		LatencyURL:    defaultSpeedTestDownloadURL,
		DownloadURL:   defaultSpeedTestDownloadURL,
		UploadURL:     defaultSpeedTestUploadURL,
		DownloadSize:  speedTestDownloadBytes,
		UploadSize:    speedTestUploadBytes,
		Workers:       speedTestWorkers,
		Client:        client,
		LatencyParam:  "bytes",
		DownloadParam: "bytes",
		UploadParam:   "bytes",
		DownloadUnit:  1,
		UploadUnit:    1,
	}
}

type speedTestRUNearestResponse struct {
	Data []struct {
		ID     int64  `json:"id"`
		Source string `json:"src"`
		Port   int    `json:"port"`
		Name   string `json:"name"`
		City   string `json:"city"`
		Owner  string `json:"source"`
	} `json:"data"`
}

type speedTestRUTokenResponse struct {
	Token string `json:"token"`
}

type speedTestRUServer struct {
	BaseURL string
	Name    string
	City    string
	Owner   string
}

func prepareSpeedTestConfig(ctx context.Context, provider string) (speedTestConfig, error) {
	client := newSpeedTestHTTPClient(speedTestWorkers)
	if provider == speedTestProviderSpeedtestRU {
		return prepareSpeedTestRUConfig(ctx, speedTestRUAPIBase, client)
	}
	return cloudflareSpeedTestConfig(client), nil
}

func prepareSpeedTestRUConfig(ctx context.Context, apiBase string, client *http.Client) (speedTestConfig, error) {
	var nearest speedTestRUNearestResponse
	if err := speedTestRUAPIRequest(ctx, client, http.MethodGet, apiBase+"/api/nearest_servers", &nearest); err != nil {
		return speedTestConfig{}, fmt.Errorf("Speedtest.ru: список серверов: %w", err)
	}
	if len(nearest.Data) == 0 {
		return speedTestConfig{}, errors.New("Speedtest.ru не вернул доступные серверы")
	}
	var token speedTestRUTokenResponse
	if err := speedTestRUAPIRequest(ctx, client, http.MethodPost, apiBase+"/api/server/gentoken", &token); err != nil {
		return speedTestConfig{}, fmt.Errorf("Speedtest.ru: авторизация: %w", err)
	}
	if token.Token == "" {
		return speedTestConfig{}, errors.New("Speedtest.ru вернул пустой токен")
	}

	candidates := make([]speedTestRUServer, 0, min(6, len(nearest.Data)))
	for _, item := range nearest.Data {
		baseURL, err := speedTestRUBaseURL(item.Source, item.Port)
		if err != nil {
			continue
		}
		candidates = append(candidates, speedTestRUServer{BaseURL: baseURL, Name: item.Name, City: item.City, Owner: item.Owner})
		if len(candidates) == 6 {
			break
		}
	}
	if len(candidates) == 0 {
		return speedTestConfig{}, errors.New("Speedtest.ru вернул некорректные адреса серверов")
	}
	headers := make(http.Header)
	headers.Set("jwt", token.Token)
	server, err := fastestSpeedTestRUServer(ctx, client, candidates, headers)
	if err != nil {
		return speedTestConfig{}, fmt.Errorf("Speedtest.ru: проверка серверов: %w", err)
	}
	label := "Speedtest.ru"
	if server.Owner != "" {
		label += " · " + server.Owner
	}
	if server.City != "" {
		label += " · " + server.City
	}
	return speedTestConfig{
		Server:        label,
		LatencyURL:    server.BaseURL + "/ping.php",
		DownloadURL:   server.BaseURL + "/download.php",
		UploadURL:     server.BaseURL + "/upload.php",
		DownloadSize:  100_000_000,
		UploadSize:    40_000_000,
		Workers:       speedTestWorkers,
		Client:        client,
		Headers:       headers,
		DownloadParam: "ckSize",
		DownloadUnit:  1_000_000,
		CacheBust:     true,
	}, nil
}

func speedTestRUAPIRequest(ctx context.Context, client *http.Client, method, endpoint string, target any) error {
	requestURL := speedTestRequestURL(endpoint, "", 0, 1, true)
	request, err := http.NewRequestWithContext(ctx, method, requestURL, nil)
	if err != nil {
		return err
	}
	request.Header.Set("x-api-key", speedTestRUAPIKey)
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", response.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(target); err != nil {
		return err
	}
	return nil
}

func speedTestRUBaseURL(source string, port int) (string, error) {
	parsed, err := url.Parse(source)
	if err != nil || parsed.Scheme == "" || parsed.Hostname() == "" || port < 1 || port > 65535 {
		return "", errors.New("некорректный адрес сервера")
	}
	parsed.Host = net.JoinHostPort(parsed.Hostname(), strconv.Itoa(port))
	parsed.Path = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}

func fastestSpeedTestRUServer(ctx context.Context, client *http.Client, candidates []speedTestRUServer, headers http.Header) (speedTestRUServer, error) {
	type result struct {
		server  speedTestRUServer
		latency time.Duration
		err     error
	}
	probeCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	results := make(chan result, len(candidates))
	for _, candidate := range candidates {
		go func(server speedTestRUServer) {
			started := time.Now()
			request, err := http.NewRequestWithContext(probeCtx, http.MethodGet, speedTestRequestURL(server.BaseURL+"/ping.php", "", 0, 1, true), nil)
			if err == nil {
				applySpeedTestHeaders(request, headers)
				var response *http.Response
				response, err = client.Do(request)
				if err == nil {
					_, copyErr := io.Copy(io.Discard, response.Body)
					response.Body.Close()
					if copyErr != nil {
						err = copyErr
					} else if response.StatusCode < 200 || response.StatusCode >= 300 {
						err = fmt.Errorf("HTTP %d", response.StatusCode)
					}
				}
			}
			results <- result{server: server, latency: time.Since(started), err: err}
		}(candidate)
	}
	var best result
	found := false
	for range candidates {
		probe := <-results
		if probe.err == nil && (!found || probe.latency < best.latency) {
			best = probe
			found = true
		}
	}
	if !found {
		return speedTestRUServer{}, errors.New("ни один ближайший сервер не ответил")
	}
	return best.server, nil
}

func runTunnelSpeedTest(ctx context.Context, cfg speedTestConfig, progress func(speedTestStatus)) (speedTestResult, error) {
	if cfg.Client == nil || cfg.Workers < 1 || cfg.DownloadSize < 1 || cfg.UploadSize < 1 {
		return speedTestResult{}, errors.New("некорректные параметры теста скорости")
	}
	if cfg.LatencyURL == "" {
		cfg.LatencyURL = cfg.DownloadURL
	}
	latency, jitter, err := measureSpeedTestLatency(ctx, cfg)
	if err != nil {
		return speedTestResult{}, fmt.Errorf("проверка задержки: %w", err)
	}
	result := speedTestResult{LatencyMS: latency, JitterMS: jitter}
	progress(speedTestStatus{Phase: "latency", Message: "Задержка измерена", Progress: 10, LatencyMS: latency, JitterMS: jitter})

	downloaded, elapsed, err := runSpeedTestPhase(ctx, cfg, http.MethodGet, cfg.DownloadURL, cfg.DownloadSize, cfg.Workers,
		func(done int64) {
			progress(speedTestStatus{Phase: "download", Message: "Измеряем скачивание", Progress: 10 + int(done*55/cfg.DownloadSize), LatencyMS: latency, JitterMS: jitter, DownloadBytes: done})
		})
	if err != nil {
		return result, fmt.Errorf("скачивание: %w", err)
	}
	result.DownloadBytes = downloaded
	result.DownloadMbps = megabitsPerSecond(downloaded, elapsed)
	progress(speedTestStatus{Phase: "download", Message: "Скачивание измерено", Progress: 65, LatencyMS: latency, JitterMS: jitter, DownloadMbps: result.DownloadMbps, DownloadBytes: downloaded})

	uploaded, elapsed, err := runSpeedTestPhase(ctx, cfg, http.MethodPost, cfg.UploadURL, cfg.UploadSize, cfg.Workers,
		func(done int64) {
			progress(speedTestStatus{Phase: "upload", Message: "Измеряем отправку", Progress: 65 + int(done*35/cfg.UploadSize), LatencyMS: latency, JitterMS: jitter, DownloadMbps: result.DownloadMbps, DownloadBytes: downloaded, UploadBytes: done})
		})
	if err != nil {
		return result, fmt.Errorf("отправка: %w", err)
	}
	result.UploadBytes = uploaded
	result.UploadMbps = megabitsPerSecond(uploaded, elapsed)
	return result, nil
}

func measureSpeedTestLatency(ctx context.Context, cfg speedTestConfig) (float64, float64, error) {
	values := make([]float64, 0, 5)
	for i := 0; i < 6; i++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, speedTestRequestURL(cfg.LatencyURL, cfg.LatencyParam, 0, 1, cfg.CacheBust), nil)
		if err != nil {
			return 0, 0, err
		}
		started := time.Now()
		applySpeedTestHeaders(req, cfg.Headers)
		resp, err := cfg.Client.Do(req)
		if err != nil {
			return 0, 0, err
		}
		_, copyErr := io.Copy(io.Discard, resp.Body)
		closeErr := resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return 0, 0, fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		if copyErr != nil {
			return 0, 0, copyErr
		}
		if closeErr != nil {
			return 0, 0, closeErr
		}
		if i > 0 {
			values = append(values, float64(time.Since(started).Microseconds())/1000)
		}
	}
	sort.Float64s(values)
	median := values[len(values)/2]
	var deviation float64
	for _, value := range values {
		if value >= median {
			deviation += value - median
		} else {
			deviation += median - value
		}
	}
	return median, deviation / float64(len(values)), nil
}

func runSpeedTestPhase(ctx context.Context, cfg speedTestConfig, method, endpoint string, totalBytes int64, workers int, progress func(int64)) (int64, time.Duration, error) {
	phaseLimit := cfg.PhaseLimit
	if phaseLimit <= 0 {
		phaseLimit = 25 * time.Second
	}
	phaseCtx, cancel := context.WithTimeout(ctx, phaseLimit)
	defer cancel()
	var transferred atomic.Int64
	var firstErr error
	var errMu sync.Mutex
	var wg sync.WaitGroup
	started := time.Now()

	base := totalBytes / int64(workers)
	remainder := totalBytes % int64(workers)
	for i := 0; i < workers; i++ {
		size := base
		if int64(i) < remainder {
			size++
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := transferSpeedTestWorker(phaseCtx, cfg, method, endpoint, size, &transferred); err != nil {
				errMu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				errMu.Unlock()
			}
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	finish := func() (int64, time.Duration, error) {
		bytes := transferred.Load()
		progress(bytes)
		elapsed := time.Since(started)
		if err := ctx.Err(); err != nil {
			return bytes, elapsed, err
		}
		if errors.Is(phaseCtx.Err(), context.DeadlineExceeded) && bytes >= min(int64(1<<20), totalBytes) &&
			(firstErr == nil || errors.Is(firstErr, context.Canceled) || errors.Is(firstErr, context.DeadlineExceeded)) {
			return bytes, elapsed, nil
		}
		if firstErr != nil {
			return bytes, elapsed, firstErr
		}
		return bytes, elapsed, phaseCtx.Err()
	}
	for {
		select {
		case <-done:
			return finish()
		case <-ticker.C:
			progress(transferred.Load())
		case <-ctx.Done():
			cancel()
			<-done
			return finish()
		case <-phaseCtx.Done():
			<-done
			return finish()
		}
	}
}

func transferSpeedTestWorker(ctx context.Context, cfg speedTestConfig, method, endpoint string, size int64, transferred *atomic.Int64) error {
	if method == http.MethodPost {
		uploadChunkSize := int64(1 << 20)
		if cfg.UploadParam == "" {
			uploadChunkSize = size
		}
		for remaining := size; remaining > 0; {
			chunk := min(remaining, uploadChunkSize)
			var err error
			for attempt := 0; attempt < 4; attempt++ {
				err = transferSpeedTestRequest(ctx, cfg, method, endpoint, chunk, transferred)
				if err == nil {
					break
				}
				if ctx.Err() != nil {
					return ctx.Err()
				}
				timer := time.NewTimer(time.Duration(attempt+1) * 100 * time.Millisecond)
				select {
				case <-timer.C:
				case <-ctx.Done():
					timer.Stop()
					return ctx.Err()
				}
			}
			if err != nil {
				return err
			}
			remaining -= chunk
		}
		return nil
	}
	return transferSpeedTestRequest(ctx, cfg, method, endpoint, size, transferred)
}

func transferSpeedTestRequest(ctx context.Context, cfg speedTestConfig, method, endpoint string, size int64, transferred *atomic.Int64) error {
	var body io.Reader
	requestURL := speedTestRequestURL(endpoint, cfg.DownloadParam, size, cfg.DownloadUnit, cfg.CacheBust)
	if method == http.MethodGet {
		requestURL = speedTestRequestURL(endpoint, cfg.DownloadParam, size, cfg.DownloadUnit, cfg.CacheBust)
	} else {
		requestURL = speedTestRequestURL(endpoint, cfg.UploadParam, size, cfg.UploadUnit, cfg.CacheBust)
		body = io.LimitReader(zeroReader{}, size)
	}
	req, err := http.NewRequestWithContext(ctx, method, requestURL, body)
	if err != nil {
		return err
	}
	if method == http.MethodPost {
		applySpeedTestHeaders(req, cfg.Headers)
		req.ContentLength = size
		req.Header.Set("Content-Type", "application/octet-stream")
		requestBytes := &atomic.Int64{}
		req.Body = io.NopCloser(&countingReader{reader: body, count: requestBytes})
		resp, err := cfg.Client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		if _, err = io.Copy(io.Discard, resp.Body); err != nil {
			return err
		}
		transferred.Add(requestBytes.Load())
		return nil
	}
	applySpeedTestHeaders(req, cfg.Headers)
	resp, err := cfg.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	_, err = io.CopyBuffer(&countingWriter{count: transferred}, resp.Body, make([]byte, 128<<10))
	return err
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

type countingReader struct {
	reader io.Reader
	count  *atomic.Int64
}

func (r *countingReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.count.Add(int64(n))
	return n, err
}

type countingWriter struct{ count *atomic.Int64 }

func (w *countingWriter) Write(p []byte) (int, error) {
	w.count.Add(int64(len(p)))
	return len(p), nil
}

func speedTestDownloadURL(endpoint string, size int64) string {
	return speedTestRequestURL(endpoint, "bytes", size, 1, false)
}

func speedTestRequestURL(endpoint, parameter string, size, unit int64, cacheBust bool) string {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return endpoint
	}
	query := parsed.Query()
	if parameter != "" {
		if unit < 1 {
			unit = 1
		}
		query.Set(parameter, fmt.Sprint((size+unit-1)/unit))
	}
	if cacheBust {
		query.Set("r", fmt.Sprint(time.Now().UnixMilli()))
	}
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func applySpeedTestHeaders(request *http.Request, headers http.Header) {
	for name, values := range headers {
		for _, value := range values {
			request.Header.Add(name, value)
		}
	}
}

func megabitsPerSecond(bytes int64, elapsed time.Duration) float64 {
	if bytes <= 0 || elapsed <= 0 {
		return 0
	}
	return float64(bytes*8) / elapsed.Seconds() / 1_000_000
}

func trafficDelta(after, before uint64) uint64 {
	if after <= before {
		return 0
	}
	return after - before
}
