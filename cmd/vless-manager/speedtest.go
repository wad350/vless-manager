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
	workerClients []*http.Client
	sampleEnd     time.Time
	uploadAck     *atomic.Int64
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
		if !s.pm.TunnelRunning() {
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
		client, clientErr := s.api.pm.tunnelSpeedTestClient(speedTestWorkers)
		if clientErr != nil {
			return clientErr
		}
		defer client.CloseIdleConnections()
		result, runErr := s.runWithServerSelection(ctx, provider, client, report)
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

func (s *speedTestService) runWithServerSelection(ctx context.Context, provider string, client *http.Client, report func(operationProgress)) (speedTestResult, error) {
	excluded := make(map[string]bool)
	const attempts = 3
	for attempt := 0; attempt < attempts; attempt++ {
		var cfg speedTestConfig
		var err error
		if provider == speedTestProviderSpeedtestRU {
			cfg, err = prepareSpeedTestRUConfigExcluding(ctx, speedTestRUAPIBase, client, excluded)
		} else {
			cfg, err = prepareSpeedTestConfigWithClient(ctx, provider, client)
		}
		var result speedTestResult
		hasData := false
		if err == nil {
			s.update(func(status *speedTestStatus) {
				status.Server = cfg.Server
				status.Message = "Проверяем задержку"
			})
			result, err = s.run(ctx, cfg, func(progress speedTestStatus) {
				hasData = hasData || progress.DownloadBytes > 0 || progress.UploadBytes > 0
				s.recordProgress(progress)
				report(operationProgress{Done: progress.Progress, Total: 100, Message: progress.Message})
			})
		}
		if err == nil {
			return result, nil
		}
		// Retry connection preparation only before payload transfer begins.
		// Never replace a failed in-progress sample with an unrelated result.
		if attempt+1 == attempts || ctx.Err() != nil || !s.api.pm.TunnelRunning() || hasData ||
			result.DownloadBytes > 0 || result.UploadBytes > 0 || !speedTestRetryableError(err) {
			return result, err
		}
		message := "Повторяем подключение к Cloudflare"
		if provider == speedTestProviderSpeedtestRU {
			message = "Выбираем другой сервер Speedtest.ru"
			if parsed, parseErr := url.Parse(cfg.LatencyURL); parseErr == nil && parsed.Host != "" {
				excluded[(&url.URL{Scheme: parsed.Scheme, Host: parsed.Host}).String()] = true
			}
		}
		client.CloseIdleConnections()
		s.api.pm.event(serviceLogWarn, "speedtest", "setup.retry", "подготовка теста повторяется после сетевой ошибки",
			field("provider", provider), field("attempt", attempt+1), field("error", err))
		report(operationProgress{Total: 100, Message: message})
		s.update(func(status *speedTestStatus) {
			status.Phase = "prepare"
			status.Message = message
			status.Progress = 0
		})
	}
	return speedTestResult{}, errors.New("не удалось выбрать сервер теста скорости")
}

func speedTestRetryableError(err error) bool {
	var networkError net.Error
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		(errors.As(err, &networkError) && networkError.Timeout())
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

func newSpeedTestHTTPClient(workers int) *http.Client {
	transport := &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		DialContext:         (&net.Dialer{Timeout: 8 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConns:        max(workers, 8),
		MaxIdleConnsPerHost: workers,
		MaxConnsPerHost:     workers,
		// Pools belong to one test and are closed when it ends. Keep an early
		// finishing worker's socket until the next phase, even if others lag.
		IdleConnTimeout:       0,
		TLSHandshakeTimeout:   8 * time.Second,
		ResponseHeaderTimeout: 8 * time.Second,
		DisableCompression:    true,
	}
	return &http.Client{Transport: transport}
}

func (pm *ProcessManager) tunnelSpeedTestClient(workers int) (*http.Client, error) {
	pm.mu.Lock()
	engine, ok := pm.box.(*embeddedXray)
	running := pm.running
	pm.mu.Unlock()
	if !running || !ok {
		return nil, errors.New("VPN выключен")
	}
	client := newSpeedTestHTTPClient(workers)
	transport := client.Transport.(*http.Transport)
	transport.Proxy = nil
	transport.DialContext = engine.DialContext
	transport.DialTLSContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		var attempts atomic.Int32
		dial := func(ctx context.Context, network, address string) (net.Conn, error) {
			if attempts.Add(1) == 2 {
				pm.event(serviceLogDebug, "speedtest", "tls.spare_started", "начато резервное TLS-подключение", field("target", address))
			}
			return engine.DialContext(ctx, network, address)
		}
		conn, err := dialSpeedTestTLS(ctx, network, address, dial,
			transport.TLSClientConfig, transport.TLSHandshakeTimeout, 1500*time.Millisecond)
		if attempts.Load() > 1 && err == nil {
			pm.event(serviceLogDebug, "speedtest", "tls.connected", "TLS-подключение установлено", field("target", address), field("dial_attempts", attempts.Load()))
		}
		return conn, err
	}
	return client, nil
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

func prepareSpeedTestConfigWithClient(ctx context.Context, provider string, client *http.Client) (speedTestConfig, error) {
	if provider == speedTestProviderSpeedtestRU {
		return prepareSpeedTestRUConfig(ctx, speedTestRUAPIBase, client)
	}
	return cloudflareSpeedTestConfig(client), nil
}

func prepareSpeedTestRUConfig(ctx context.Context, apiBase string, client *http.Client) (speedTestConfig, error) {
	return prepareSpeedTestRUConfigExcluding(ctx, apiBase, client, nil)
}

func prepareSpeedTestRUConfigExcluding(ctx context.Context, apiBase string, client *http.Client, excluded map[string]bool) (speedTestConfig, error) {
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
		if err != nil || excluded[baseURL] {
			continue
		}
		candidates = append(candidates, speedTestRUServer{BaseURL: baseURL, Name: item.Name, City: item.City, Owner: item.Owner})
		if len(candidates) == 6 {
			break
		}
	}
	if len(candidates) == 0 {
		return speedTestConfig{}, errors.New("Speedtest.ru не вернул других подходящих серверов")
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
	clients, closeClients, err := prepareSpeedTestWorkers(ctx, cfg, cfg.Workers)
	if err != nil {
		return result, fmt.Errorf("подготовка соединений: %w", err)
	}
	defer closeClients()
	cfg.workerClients = clients

	downloaded, elapsed, err := runSpeedTestPhase(ctx, cfg, http.MethodGet, cfg.DownloadURL, cfg.DownloadSize, cfg.Workers,
		func(done int64) {
			progress(speedTestStatus{Phase: "download", Message: "Измеряем скачивание", Progress: min(65, 10+int(done*55/cfg.DownloadSize)), LatencyMS: latency, JitterMS: jitter, DownloadBytes: done})
		})
	result.DownloadBytes = downloaded
	if err != nil {
		return result, fmt.Errorf("скачивание: %w", err)
	}
	result.DownloadMbps = megabitsPerSecond(downloaded, elapsed)
	progress(speedTestStatus{Phase: "download", Message: "Скачивание измерено", Progress: 65, LatencyMS: latency, JitterMS: jitter, DownloadMbps: result.DownloadMbps, DownloadBytes: downloaded})

	uploaded, elapsed, err := runSpeedTestPhase(ctx, cfg, http.MethodPost, cfg.UploadURL, cfg.UploadSize, cfg.Workers,
		func(done int64) {
			progress(speedTestStatus{Phase: "upload", Message: "Измеряем отправку", Progress: min(100, 65+int(done*35/cfg.UploadSize)), LatencyMS: latency, JitterMS: jitter, DownloadMbps: result.DownloadMbps, DownloadBytes: downloaded, UploadBytes: done})
		})
	result.UploadBytes = uploaded
	if err != nil {
		return result, fmt.Errorf("отправка: %w", err)
	}
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
	clients := cfg.workerClients
	if len(clients) != workers {
		var closeClients func()
		var err error
		clients, closeClients, err = prepareSpeedTestWorkers(ctx, cfg, workers)
		if err != nil {
			return 0, 0, fmt.Errorf("подготовка соединений: %w", err)
		}
		defer closeClients()
	}
	phaseLimit := cfg.PhaseLimit
	if phaseLimit <= 0 {
		phaseLimit = 25 * time.Second
	}
	started := time.Now()
	requestLimit := phaseLimit
	if cfg.DownloadParam != "" {
		// Stop starting requests at the sampling boundary, then drain the last
		// small response. Aborting a large response closes its TLS connection
		// and forces the upload to handshake again on an already busy tunnel.
		cfg.sampleEnd = started.Add(phaseLimit)
		requestLimit += 8 * time.Second
	}
	phaseCtx, cancel := context.WithTimeout(ctx, requestLimit)
	defer cancel()
	var transferred atomic.Int64
	var uploadAck atomic.Int64
	cfg.uploadAck = &uploadAck
	var firstErr error
	var errMu sync.Mutex
	var wg sync.WaitGroup

	base := totalBytes / int64(workers)
	remainder := totalBytes % int64(workers)
	for i := 0; i < workers; i++ {
		size := base
		if int64(i) < remainder {
			size++
		}
		wg.Add(1)
		workerConfig := cfg
		workerConfig.Client = clients[i]
		go func() {
			defer wg.Done()
			if err := transferSpeedTestWorker(phaseCtx, workerConfig, method, endpoint, size, &transferred); err != nil {
				// Closing a TLS-backed pipe at our sampling deadline may surface as
				// EOF instead of a context error. Do not turn that into a failed test,
				// but retain EOFs received before the deadline as genuine failures.
				if phaseCtx.Err() != nil && (errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) ||
					errors.Is(err, io.ErrClosedPipe) || errors.Is(err, net.ErrClosed)) {
					err = phaseCtx.Err()
				}
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
			(method != http.MethodPost || uploadAck.Load() > 0) &&
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

func prepareSpeedTestWorkers(ctx context.Context, cfg speedTestConfig, workers int) ([]*http.Client, func(), error) {
	clients := make([]*http.Client, workers)
	transport, cloneable := cfg.Client.Transport.(*http.Transport)
	if !cloneable || cfg.LatencyURL == "" {
		for i := range clients {
			clients[i] = cfg.Client
		}
		return clients, func() {}, nil
	}
	closeClients := func() {
		for _, client := range clients {
			client.CloseIdleConnections()
		}
	}
	for i := range clients {
		if i == 0 {
			clients[i] = cfg.Client
			continue
		}
		client := *cfg.Client
		workerTransport := transport.Clone()
		workerTransport.MaxConnsPerHost = 1
		workerTransport.MaxIdleConnsPerHost = 1
		workerTransport.MaxIdleConns = 1
		client.Transport = workerTransport
		clients[i] = &client
	}
	// Reserve one connection per worker before bulk transfer. Keep the latency
	// connection for worker zero, and reuse this pool for both directions.
	for _, client := range clients {
		err := func() error {
			warmCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
			defer cancel()
			req, err := http.NewRequestWithContext(warmCtx, http.MethodGet,
				speedTestRequestURL(cfg.LatencyURL, cfg.LatencyParam, 0, 1, cfg.CacheBust), nil)
			if err != nil {
				return err
			}
			applySpeedTestHeaders(req, cfg.Headers)
			resp, err := client.Do(req)
			if err != nil {
				return err
			}
			_, err = io.Copy(io.Discard, resp.Body)
			closeErr := resp.Body.Close()
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				err = fmt.Errorf("HTTP %d", resp.StatusCode)
			} else if err == nil {
				err = closeErr
			}
			return err
		}()
		if err != nil {
			closeClients()
			return nil, func() {}, err
		}
	}
	return clients, closeClients, nil
}

func transferSpeedTestWorker(ctx context.Context, cfg speedTestConfig, method, endpoint string, size int64, transferred *atomic.Int64) error {
	if method == http.MethodPost {
		uploadChunkSize := int64(1 << 20)
		for remaining := size; remaining > 0; {
			if !cfg.sampleEnd.IsZero() && !time.Now().Before(cfg.sampleEnd) {
				return nil
			}
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
	chunkSize := size
	if cfg.DownloadParam != "" {
		unit := max(int64(1), cfg.DownloadUnit)
		chunkSize = max(unit, (1<<20)/unit*unit)
	}
	for remaining := size; remaining > 0; {
		if !cfg.sampleEnd.IsZero() && !time.Now().Before(cfg.sampleEnd) {
			return nil
		}
		chunk := min(remaining, chunkSize)
		if err := transferSpeedTestRequest(ctx, cfg, method, endpoint, chunk, transferred); err != nil {
			return err
		}
		remaining -= chunk
	}
	return nil
}

func transferSpeedTestRequest(ctx context.Context, cfg speedTestConfig, method, endpoint string, size int64, transferred *atomic.Int64) error {
	var body io.Reader
	var requestURL string
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
		req.Body = io.NopCloser(&countingReader{reader: body, count: transferred})
		resp, err := cfg.Client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			_, _ = io.CopyN(io.Discard, resp.Body, 64<<10)
			return fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		if _, err = io.Copy(io.Discard, resp.Body); err != nil {
			return err
		}
		if cfg.uploadAck != nil {
			cfg.uploadAck.Add(size)
		}
		return nil
	}
	applySpeedTestHeaders(req, cfg.Headers)
	resp, err := doSpeedTestDownloadRequest(cfg.Client, req)
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

func doSpeedTestDownloadRequest(client *http.Client, req *http.Request) (*http.Response, error) {
	resp, err := client.Do(req)
	// An unanswered GET has transferred no measured payload and is safe to
	// repeat on a fresh connection. Keep the same phase deadline and byte
	// counter; never replay a partially read body or change the test server.
	if err == nil || req.Context().Err() != nil || !speedTestRetryableError(err) {
		return resp, err
	}
	client.CloseIdleConnections()
	return client.Do(req.Clone(req.Context()))
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
