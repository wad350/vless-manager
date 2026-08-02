package main

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/net/proxy"
)

const (
	updateCheckTimeout      = 20 * time.Second
	updateFetchTimeout      = 5 * time.Minute
	updateAutoCheckInterval = time.Hour
	updateInitialCheckDelay = 15 * time.Second
	maxUpdateBytes          = 64 << 20
)

var versionPattern = regexp.MustCompile(`^v?([0-9]+)\.([0-9]+)\.([0-9]+)$`)

type UpdateStatus struct {
	CurrentVersion   string     `json:"current_version"`
	LatestVersion    string     `json:"latest_version,omitempty"`
	TargetVersion    string     `json:"target_version,omitempty"`
	Available        bool       `json:"available"`
	State            string     `json:"state"`
	Message          string     `json:"message,omitempty"`
	Progress         int        `json:"progress"`
	DownloadedBytes  int64      `json:"downloaded_bytes,omitempty"`
	TotalBytes       int64      `json:"total_bytes,omitempty"`
	BytesPerSecond   int64      `json:"bytes_per_second,omitempty"`
	Transport        string     `json:"transport,omitempty"`
	CheckedAt        *time.Time `json:"checked_at,omitempty"`
	StartedAt        *time.Time `json:"started_at,omitempty"`
	UpdatedAt        *time.Time `json:"updated_at,omitempty"`
	ReleaseURL       string     `json:"release_url,omitempty"`
	Error            string     `json:"error,omitempty"`
	CheckAttemptedAt *time.Time `json:"check_attempted_at,omitempty"`
	CheckError       string     `json:"check_error,omitempty"`
}

type githubRelease struct {
	TagName    string        `json:"tag_name"`
	HTMLURL    string        `json:"html_url"`
	Draft      bool          `json:"draft"`
	Prerelease bool          `json:"prerelease"`
	Assets     []githubAsset `json:"assets"`
}

type githubAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
}

type releaseAsset struct {
	Name string
	URL  string
	Size int64
}

type appUpdater struct {
	api *apiServer

	mu     sync.Mutex
	busy   bool
	status UpdateStatus
	path   string
}

func newAppUpdater(api *apiServer) *appUpdater {
	u := &appUpdater{
		api:  api,
		path: filepath.Join(filepath.Dir(api.cfgPath), "update-status.json"),
		status: UpdateStatus{
			CurrentVersion: Version,
			State:          "idle",
		},
	}
	u.load()
	return u
}

func (u *appUpdater) snapshot() UpdateStatus {
	u.mu.Lock()
	defer u.mu.Unlock()
	status := u.status
	status.CurrentVersion = Version
	return status
}

func (u *appUpdater) begin(state string) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.busy {
		return false
	}
	now := time.Now()
	u.busy = true
	u.status.State = state
	u.status.Message = updateStateMessage(state)
	u.status.Progress = 2
	u.status.DownloadedBytes = 0
	u.status.TotalBytes = 0
	u.status.BytesPerSecond = 0
	u.status.StartedAt = &now
	u.status.UpdatedAt = &now
	u.status.Error = ""
	return true
}

func (u *appUpdater) finish(status UpdateStatus) {
	u.mu.Lock()
	u.busy = false
	status.CurrentVersion = Version
	now := time.Now()
	status.UpdatedAt = &now
	u.status = status
	u.mu.Unlock()
}

func (u *appUpdater) load() {
	data, err := os.ReadFile(u.path)
	if err != nil {
		return
	}
	var status UpdateStatus
	if json.Unmarshal(data, &status) != nil || status.LatestVersion == "" || status.CheckedAt == nil {
		return
	}
	available, err := newerVersion(status.LatestVersion, Version)
	if err != nil && Version != "dev" {
		return
	}
	status.CurrentVersion = Version
	status.Available = available || Version == "dev"
	status.State = "ready"
	status.TargetVersion = ""
	status.Message = ""
	status.Progress = 0
	status.DownloadedBytes = 0
	status.TotalBytes = 0
	status.BytesPerSecond = 0
	status.StartedAt = nil
	status.Error = ""
	u.status = status
}

func (u *appUpdater) persist(status UpdateStatus) {
	if err := writeJSONAtomic(u.path, status); err != nil {
		u.api.pm.event(serviceLogWarn, "update", "status.persist_failed",
			"не удалось сохранить результат проверки обновлений",
			field("path", u.path),
			field("error", err))
	}
}

func (u *appUpdater) finishCheckFailure(checkErr error, transport string) UpdateStatus {
	now := time.Now()
	u.mu.Lock()
	status := u.status
	u.busy = false
	status.CurrentVersion = Version
	status.CheckAttemptedAt = &now
	status.CheckError = checkErr.Error()
	status.UpdatedAt = &now
	if status.LatestVersion != "" && status.CheckedAt != nil {
		status.State = "ready"
		status.Message = ""
		status.Progress = 0
		status.DownloadedBytes = 0
		status.TotalBytes = 0
		status.BytesPerSecond = 0
		status.StartedAt = nil
		status.Error = ""
	} else {
		status.State = "error"
		status.Error = checkErr.Error()
		if transport != "" {
			status.Transport = transport
		}
	}
	u.status = status
	u.mu.Unlock()
	u.persist(status)
	return status
}

func (u *appUpdater) nextAutoCheckDelay(now time.Time) time.Duration {
	status := u.snapshot()
	lastAttempt := status.CheckAttemptedAt
	if lastAttempt == nil {
		lastAttempt = status.CheckedAt
	}
	if lastAttempt == nil {
		return updateInitialCheckDelay
	}
	delay := lastAttempt.Add(updateAutoCheckInterval).Sub(now)
	if delay < updateInitialCheckDelay {
		return updateInitialCheckDelay
	}
	return delay
}

func (u *appUpdater) runAutoChecks() {
	for {
		time.Sleep(u.nextAutoCheckDelay(time.Now()))
		_, err := u.checkWithSource(context.Background(), "background")
		if err != nil {
			u.api.pm.event(serviceLogWarn, "update", "check.failed",
				"автоматическая проверка обновлений не выполнена; сохранён предыдущий результат",
				field("error", err))
		}
	}
}

func (u *appUpdater) updateStatus(update func(*UpdateStatus)) {
	u.mu.Lock()
	update(&u.status)
	u.status.CurrentVersion = Version
	now := time.Now()
	u.status.UpdatedAt = &now
	u.mu.Unlock()
}

func (u *appUpdater) setTransport(transport string) {
	u.updateStatus(func(status *UpdateStatus) {
		status.Transport = transport
	})
}

func (u *appUpdater) setInstallProgress(
	state, message string,
	progress int,
	downloaded, total, bytesPerSecond int64,
) {
	u.updateStatus(func(status *UpdateStatus) {
		status.State = state
		status.Message = message
		status.Progress = progress
		status.DownloadedBytes = downloaded
		status.TotalBytes = total
		status.BytesPerSecond = bytesPerSecond
	})
	u.api.operations.Progress("app-update", operationProgress{
		Done: progress, Total: 100, Message: message,
	})
}

func updateStateMessage(state string) string {
	switch state {
	case "checking":
		return "Проверяем последний релиз"
	case "checksum":
		return "Получаем контрольную сумму"
	case "downloading":
		return "Скачиваем обновление"
	case "verifying":
		return "Проверяем целостность и архитектуру"
	case "preparing":
		return "Подготавливаем установку"
	case "restarting":
		return "Перезапускаем сервис"
	default:
		return ""
	}
}

func (u *appUpdater) check(ctx context.Context) (UpdateStatus, error) {
	return u.checkWithSource(ctx, "manual")
}

func (u *appUpdater) checkWithSource(ctx context.Context, source string) (UpdateStatus, error) {
	var status UpdateStatus
	var checkErr error
	dedupeKey := ""
	if source == "background" {
		dedupeKey = "app-update-check"
	}
	err := u.api.operations.Run(ctx, operationRequest{
		Kind:        "app-update-check",
		Title:       "Проверка обновления",
		Source:      source,
		DedupeKey:   dedupeKey,
		Cancellable: true,
		StallLimit:  45 * time.Second,
	}, func(runCtx context.Context, report func(operationProgress)) error {
		report(operationProgress{Total: 1, Message: "Проверяем GitHub Releases"})
		checkCtx, cancel := context.WithTimeout(runCtx, updateCheckTimeout)
		defer cancel()
		status, checkErr = u.checkUncoordinated(checkCtx)
		if checkErr == nil {
			report(operationProgress{Done: 1, Total: 1, Message: "Проверка завершена"})
		}
		return checkErr
	})
	if err != nil {
		if checkErr != nil {
			return status, checkErr
		}
		return u.snapshot(), err
	}
	return status, nil
}

func (u *appUpdater) checkUncoordinated(ctx context.Context) (UpdateStatus, error) {
	if !u.begin("checking") {
		return u.snapshot(), errors.New("проверка обновления уже выполняется")
	}

	release, transport, err := u.fetchRelease(ctx, updateCheckTimeout)
	if err != nil {
		return u.finishCheckFailure(err, ""), err
	}
	latest, err := normalizedVersion(release.TagName)
	if err != nil {
		return u.finishCheckFailure(err, transport), err
	}
	available, err := newerVersion(latest, Version)
	if err != nil && Version != "dev" {
		return u.finishCheckFailure(err, transport), err
	}
	checkedAt := time.Now()
	status := UpdateStatus{
		CurrentVersion:   Version,
		LatestVersion:    latest,
		Available:        available || Version == "dev",
		State:            "ready",
		Transport:        transport,
		CheckedAt:        &checkedAt,
		CheckAttemptedAt: &checkedAt,
		ReleaseURL:       release.HTMLURL,
	}
	u.finish(status)
	u.persist(status)
	u.api.pm.event(serviceLogInfo, "update", "check.completed",
		"проверка обновления завершена",
		field("current_version", Version),
		field("latest_version", latest),
		field("available", status.Available),
		field("transport", transport))
	return status, nil
}

func (u *appUpdater) install(ctx context.Context) (UpdateStatus, error) {
	if !u.begin("downloading") {
		return u.snapshot(), errors.New("обновление уже выполняется")
	}
	var status UpdateStatus
	var installErr error
	err := u.api.operations.Run(ctx, operationRequest{
		Kind:        "app-update",
		Title:       "Установка обновления",
		Source:      "manual",
		Cancellable: true,
		StallLimit:  90 * time.Second,
	}, func(runCtx context.Context, report func(operationProgress)) error {
		status, installErr = u.installStarted(runCtx)
		return installErr
	})
	if err != nil && installErr == nil {
		return u.failInstall(err)
	}
	return status, installErr
}

func (u *appUpdater) startInstall() (UpdateStatus, error) {
	if !u.begin("checking") {
		return u.snapshot(), errors.New("обновление уже выполняется")
	}
	go func() {
		err := u.api.operations.Run(context.Background(), operationRequest{
			Kind:        "app-update",
			Title:       "Установка обновления",
			Source:      "manual",
			Cancellable: true,
			StallLimit:  90 * time.Second,
		}, func(runCtx context.Context, report func(operationProgress)) error {
			installCtx, cancel := context.WithTimeout(runCtx, updateFetchTimeout)
			defer cancel()
			_, installErr := u.installStarted(installCtx)
			return installErr
		})
		if err != nil && u.snapshot().State != "error" {
			_, _ = u.failInstall(err)
		}
	}()
	return u.snapshot(), nil
}

func (u *appUpdater) installStarted(ctx context.Context) (UpdateStatus, error) {
	u.setInstallProgress("checking", updateStateMessage("checking"), 4, 0, 0, 0)

	updatePath := filepath.Join(os.TempDir(), "vless-manager-update.ipk")

	var release githubRelease
	var downloadStarted time.Time
	transport, err := u.withPreferredTransport(ctx, updateFetchTimeout, func(client *http.Client) error {
		var fetchErr error
		release, fetchErr = fetchLatestRelease(ctx, client, githubReleaseAPIURL())
		if fetchErr != nil {
			return fetchErr
		}
		latest, versionErr := normalizedVersion(release.TagName)
		if versionErr != nil {
			return versionErr
		}
		available, versionErr := newerVersion(latest, Version)
		if versionErr != nil && Version != "dev" {
			return versionErr
		}
		if !available && Version != "dev" {
			return fmt.Errorf("установлена актуальная версия %s", Version)
		}
		u.updateStatus(func(status *UpdateStatus) {
			status.LatestVersion = latest
			status.TargetVersion = latest
			status.Available = true
			status.ReleaseURL = release.HTMLURL
		})
		pkg, checksum, assetErr := releaseAssets(release, latest)
		if assetErr != nil {
			return assetErr
		}
		return downloadVerifiedPackage(ctx, client, pkg, checksum, latest, updatePath,
			func(phase string, downloaded, total int64) {
				switch phase {
				case "checksum":
					u.setInstallProgress("checksum", updateStateMessage("checksum"), 8, 0, total, 0)
				case "downloading":
					if downloadStarted.IsZero() {
						downloadStarted = time.Now()
					}
					progress := 10
					if total > 0 {
						progress += int(downloaded * 65 / total)
					}
					if progress > 75 {
						progress = 75
					}
					var speed int64
					if elapsed := time.Since(downloadStarted); elapsed > 0 {
						speed = int64(float64(downloaded) / elapsed.Seconds())
					}
					u.setInstallProgress("downloading", updateStateMessage("downloading"),
						progress, downloaded, total, speed)
				case "verifying":
					u.setInstallProgress("verifying", updateStateMessage("verifying"),
						82, downloaded, total, 0)
				case "preparing":
					u.setInstallProgress("preparing", updateStateMessage("preparing"),
						90, downloaded, total, 0)
				}
			})
	})
	if err != nil {
		_ = os.Remove(updatePath)
		return u.failInstall(err)
	}

	latest, _ := normalizedVersion(release.TagName)
	if err := schedulePackageUpdate(updatePath); err != nil {
		_ = os.Remove(updatePath)
		return u.failInstall(err)
	}
	checkedAt := time.Now()
	status := UpdateStatus{
		CurrentVersion:  Version,
		LatestVersion:   latest,
		TargetVersion:   latest,
		Available:       true,
		State:           "restarting",
		Message:         updateStateMessage("restarting"),
		Progress:        96,
		DownloadedBytes: u.snapshot().DownloadedBytes,
		TotalBytes:      u.snapshot().TotalBytes,
		Transport:       transport,
		CheckedAt:       &checkedAt,
		StartedAt:       u.snapshot().StartedAt,
		ReleaseURL:      release.HTMLURL,
	}
	u.finish(status)
	u.api.pm.event(serviceLogInfo, "update", "install.scheduled",
		"обновление проверено и подготовлено, сервис будет перезапущен",
		field("current_version", Version),
		field("latest_version", latest),
		field("transport", transport))
	return status, nil
}

func (u *appUpdater) failInstall(err error) (UpdateStatus, error) {
	previous := u.snapshot()
	status := UpdateStatus{
		CurrentVersion: Version,
		LatestVersion:  previous.LatestVersion,
		TargetVersion:  previous.TargetVersion,
		State:          "error",
		Message:        "Обновление не установлено",
		Progress:       previous.Progress,
		Transport:      previous.Transport,
		StartedAt:      previous.StartedAt,
		ReleaseURL:     previous.ReleaseURL,
		Error:          err.Error(),
	}
	u.finish(status)
	u.api.pm.event(serviceLogError, "update", "install.failed",
		"обновление приложения не установлено", field("error", err))
	return status, err
}

func (u *appUpdater) fetchRelease(ctx context.Context, timeout time.Duration) (githubRelease, string, error) {
	var release githubRelease
	transport, err := u.withPreferredTransport(ctx, timeout, func(client *http.Client) error {
		var fetchErr error
		release, fetchErr = fetchLatestRelease(ctx, client, githubReleaseAPIURL())
		return fetchErr
	})
	return release, transport, err
}

// withPreferredTransport uses the active tunnel first whenever VPN is
// running. A failed VPN request is retried over a WAN-marked socket so a
// broken tunnel cannot prevent recovery.
func (u *appUpdater) withPreferredTransport(
	ctx context.Context,
	timeout time.Duration,
	fn func(*http.Client) error,
) (string, error) {
	if u.api.pm.Status().Running && u.api.pm.TunRunning() {
		u.setTransport("vpn")
		dialer, err := proxy.SOCKS5("tcp",
			fmt.Sprintf("127.0.0.1:%d", socksHealthPort), nil, proxy.Direct)
		if err == nil {
			if err = fn(updateHTTPClient(timeout, dialer.Dial)); err == nil {
				return "vpn", nil
			}
			u.api.pm.event(serviceLogWarn, "update", "request.fallback",
				"загрузка обновления через VPN не удалась, выполняется повтор через WAN",
				field("error", err))
		}
	}
	u.setTransport("wan")
	if err := fn(updateHTTPClient(timeout, wanDialer(timeout).Dial)); err != nil {
		return "wan", err
	}
	return "wan", nil
}

func updateHTTPClient(timeout time.Duration, dial func(string, string) (net.Conn, error)) *http.Client {
	transport := &http.Transport{
		Dial:                  dial,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 20 * time.Second,
		DisableKeepAlives:     true,
	}
	return &http.Client{Timeout: timeout, Transport: transport}
}

func githubReleaseAPIURL() string {
	return "https://api.github.com/repos/" + UpdateRepository + "/releases/latest"
}

func fetchLatestRelease(ctx context.Context, client *http.Client, endpoint string) (githubRelease, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return githubRelease{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "vless-manager/"+Version)
	resp, err := client.Do(req)
	if err != nil {
		return githubRelease{}, fmt.Errorf("GitHub Release: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return githubRelease{}, fmt.Errorf("GitHub Release: HTTP %d", resp.StatusCode)
	}
	var release githubRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&release); err != nil {
		return githubRelease{}, fmt.Errorf("GitHub Release: %w", err)
	}
	if release.Draft || release.Prerelease || release.TagName == "" {
		return githubRelease{}, errors.New("GitHub вернул неподходящий релиз")
	}
	return release, nil
}

func releaseAssets(release githubRelease, version string) (releaseAsset, releaseAsset, error) {
	packageName := "vless-manager_" + version + "_mipsel-3.4.ipk"
	checksumName := packageName + ".sha256"
	var pkg, checksum releaseAsset
	for _, asset := range release.Assets {
		candidate := releaseAsset{Name: asset.Name, URL: asset.BrowserDownloadURL, Size: asset.Size}
		switch asset.Name {
		case packageName:
			pkg = candidate
		case checksumName:
			checksum = candidate
		}
	}
	if pkg.URL == "" || checksum.URL == "" {
		return releaseAsset{}, releaseAsset{}, fmt.Errorf(
			"в релизе %s отсутствуют %s или его SHA-256", release.TagName, packageName)
	}
	if pkg.Size <= 0 || pkg.Size > maxUpdateBytes {
		return releaseAsset{}, releaseAsset{}, fmt.Errorf("недопустимый размер обновления: %d", pkg.Size)
	}
	if err := validateGitHubAssetURL(pkg.URL); err != nil {
		return releaseAsset{}, releaseAsset{}, err
	}
	if err := validateGitHubAssetURL(checksum.URL); err != nil {
		return releaseAsset{}, releaseAsset{}, err
	}
	return pkg, checksum, nil
}

func validateGitHubAssetURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" {
		return errors.New("GitHub вернул некорректный URL файла")
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "github.com" && !strings.HasSuffix(host, ".github.com") &&
		host != "githubusercontent.com" && !strings.HasSuffix(host, ".githubusercontent.com") {
		return fmt.Errorf("неожиданный сервер обновления %q", host)
	}
	return nil
}

func downloadVerifiedPackage(
	ctx context.Context,
	client *http.Client,
	pkg releaseAsset,
	checksum releaseAsset,
	version string,
	destination string,
	progress func(phase string, downloaded, total int64),
) error {
	if progress != nil {
		progress("checksum", 0, pkg.Size)
	}
	expected, err := fetchChecksum(ctx, client, checksum.URL)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pkg.URL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "vless-manager/"+Version)
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("загрузка обновления: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("загрузка обновления: HTTP %d", resp.StatusCode)
	}

	tmp := destination + ".part"
	_ = os.Remove(tmp)
	file, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0755)
	if err != nil {
		return err
	}
	hash := sha256.New()
	if progress != nil {
		progress("downloading", 0, pkg.Size)
	}
	writer := &updateProgressWriter{
		writer: io.MultiWriter(file, hash),
		total:  pkg.Size,
		report: func(written, total int64) {
			if progress != nil {
				progress("downloading", written, total)
			}
		},
	}
	written, copyErr := io.Copy(writer, io.LimitReader(resp.Body, maxUpdateBytes+1))
	closeErr := file.Close()
	if copyErr != nil {
		_ = os.Remove(tmp)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return closeErr
	}
	if written > maxUpdateBytes {
		_ = os.Remove(tmp)
		return errors.New("файл обновления превышает допустимый размер")
	}
	if progress != nil {
		progress("verifying", written, pkg.Size)
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if !strings.EqualFold(actual, expected) {
		_ = os.Remove(tmp)
		return errors.New("SHA-256 обновления не совпадает")
	}
	if err := validateIPK(tmp, version); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if progress != nil {
		progress("preparing", written, pkg.Size)
	}
	_ = os.Remove(destination)
	return os.Rename(tmp, destination)
}

type updateProgressWriter struct {
	writer  io.Writer
	total   int64
	written int64
	report  func(written, total int64)
}

func (w *updateProgressWriter) Write(data []byte) (int, error) {
	n, err := w.writer.Write(data)
	w.written += int64(n)
	if w.report != nil {
		w.report(w.written, w.total)
	}
	return n, err
}

func fetchChecksum(ctx context.Context, client *http.Client, rawURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "vless-manager/"+Version)
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("загрузка SHA-256: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("загрузка SHA-256: HTTP %d", resp.StatusCode)
	}
	scanner := bufio.NewScanner(io.LimitReader(resp.Body, 4096))
	if !scanner.Scan() {
		return "", errors.New("пустой файл SHA-256")
	}
	value := strings.Fields(scanner.Text())
	if len(value) == 0 || len(value[0]) != sha256.Size*2 {
		return "", errors.New("некорректный SHA-256")
	}
	if _, err := hex.DecodeString(value[0]); err != nil {
		return "", errors.New("некорректный SHA-256")
	}
	return strings.ToLower(value[0]), nil
}

func validateIPK(path, version string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("не удалось открыть IPK: %w", err)
	}
	defer file.Close()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return fmt.Errorf("обновление не является IPK: %w", err)
	}
	defer gzipReader.Close()

	var debianBinary, controlOK, managerBinary bool
	outer := tar.NewReader(gzipReader)
	for {
		header, nextErr := outer.Next()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			return fmt.Errorf("некорректный IPK: %w", nextErr)
		}
		switch strings.TrimPrefix(header.Name, "./") {
		case "debian-binary":
			data, readErr := io.ReadAll(io.LimitReader(outer, 16))
			if readErr != nil {
				return fmt.Errorf("чтение версии IPK: %w", readErr)
			}
			debianBinary = string(data) == "2.0\n"
		case "control.tar.gz":
			controlOK, err = validateIPKControl(outer, version)
			if err != nil {
				return err
			}
		case "data.tar.gz":
			managerBinary, err = validateIPKData(outer)
			if err != nil {
				return err
			}
		}
	}
	if !debianBinary || !controlOK || !managerBinary {
		return fmt.Errorf("IPK не прошёл проверку: format=%v metadata=%v binary=%v",
			debianBinary, controlOK, managerBinary)
	}
	return nil
}

func validateIPKControl(reader io.Reader, version string) (bool, error) {
	gzipReader, err := gzip.NewReader(reader)
	if err != nil {
		return false, fmt.Errorf("некорректный control.tar.gz: %w", err)
	}
	defer gzipReader.Close()
	archive := tar.NewReader(gzipReader)
	for {
		header, nextErr := archive.Next()
		if errors.Is(nextErr, io.EOF) {
			return false, nil
		}
		if nextErr != nil {
			return false, fmt.Errorf("чтение control.tar.gz: %w", nextErr)
		}
		if strings.TrimPrefix(header.Name, "./") != "control" {
			continue
		}
		data, readErr := io.ReadAll(io.LimitReader(archive, 64<<10))
		if readErr != nil {
			return false, fmt.Errorf("чтение метаданных IPK: %w", readErr)
		}
		fields := parsePackageControl(data)
		if fields["Package"] != "vless-manager" ||
			fields["Version"] != version ||
			fields["Architecture"] != "mipsel-3.4" {
			return false, fmt.Errorf(
				"неподходящий IPK: package=%q version=%q architecture=%q",
				fields["Package"], fields["Version"], fields["Architecture"])
		}
		return true, nil
	}
}

func parsePackageControl(data []byte) map[string]string {
	fields := make(map[string]string)
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := scanner.Text()
		name, value, found := strings.Cut(line, ":")
		if found && !strings.HasPrefix(line, " ") {
			fields[strings.TrimSpace(name)] = strings.TrimSpace(value)
		}
	}
	return fields
}

func validateIPKData(reader io.Reader) (bool, error) {
	gzipReader, err := gzip.NewReader(reader)
	if err != nil {
		return false, fmt.Errorf("некорректный data.tar.gz: %w", err)
	}
	defer gzipReader.Close()
	archive := tar.NewReader(gzipReader)
	for {
		header, nextErr := archive.Next()
		if errors.Is(nextErr, io.EOF) {
			return false, nil
		}
		if nextErr != nil {
			return false, fmt.Errorf("чтение data.tar.gz: %w", nextErr)
		}
		if strings.TrimPrefix(header.Name, "./") != "opt/bin/vless-manager" {
			continue
		}
		elfHeader := make([]byte, 20)
		if _, err := io.ReadFull(archive, elfHeader); err != nil {
			return false, fmt.Errorf("бинарник в IPK повреждён: %w", err)
		}
		if !bytes.Equal(elfHeader[:4], []byte{0x7f, 'E', 'L', 'F'}) ||
			elfHeader[4] != 1 ||
			elfHeader[5] != 1 ||
			elfHeader[18] != 8 ||
			elfHeader[19] != 0 {
			return false, errors.New("бинарник в IPK не является MIPSLE ELF32")
		}
		return true, nil
	}
}

func schedulePackageUpdate(updatePath string) error {
	initScript := "/opt/etc/init.d/S99vless-manager"
	if _, err := os.Stat(initScript); err != nil {
		return fmt.Errorf("init-скрипт не найден: %w", err)
	}
	opkgPath := "/opt/bin/opkg"
	if _, err := os.Stat(opkgPath); err != nil {
		return fmt.Errorf("opkg не найден: %w", err)
	}
	scriptPath := "/tmp/vless-manager-apply-update.sh"
	logPath := "/opt/var/log/vless-manager-update.log"
	script := fmt.Sprintf(`#!/bin/sh
sleep 2
PKG=%s
INIT=%s
OPKG=%s
LOG=%s
echo "[$(date '+%%Y-%%m-%%d %%H:%%M:%%S')] installing $PKG via opkg" >>"$LOG"
if "$OPKG" install --force-reinstall "$PKG" >>"$LOG" 2>&1; then
  sleep 4
  if "$INIT" status >>"$LOG" 2>&1; then
    rm -f "$PKG" "$0"
    exit 0
  fi
fi
"$INIT" start >>"$LOG" 2>&1
rm -f "$PKG" "$0"
exit 1
`, shellQuote(updatePath), shellQuote(initScript), shellQuote(opkgPath), shellQuote(logPath))
	if err := os.WriteFile(scriptPath, []byte(script), 0700); err != nil {
		return err
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	cmd := exec.Command("/bin/sh", scriptPath)
	cmd.Stdin = nil
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		logFile.Close()
		return err
	}
	_ = cmd.Process.Release()
	return logFile.Close()
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func normalizedVersion(value string) (string, error) {
	match := versionPattern.FindStringSubmatch(strings.TrimSpace(value))
	if match == nil {
		return "", fmt.Errorf("некорректная версия %q", value)
	}
	return strings.TrimPrefix(match[0], "v"), nil
}

func newerVersion(candidate, current string) (bool, error) {
	candidateParts, err := versionParts(candidate)
	if err != nil {
		return false, err
	}
	currentParts, err := versionParts(current)
	if err != nil {
		return false, err
	}
	for i := range candidateParts {
		if candidateParts[i] != currentParts[i] {
			return candidateParts[i] > currentParts[i], nil
		}
	}
	return false, nil
}

func versionParts(value string) ([3]int, error) {
	var parts [3]int
	match := versionPattern.FindStringSubmatch(strings.TrimSpace(value))
	if match == nil {
		return parts, fmt.Errorf("некорректная версия %q", value)
	}
	for i := range parts {
		number, err := strconv.Atoi(match[i+1])
		if err != nil {
			return parts, err
		}
		parts[i] = number
	}
	return parts, nil
}

func (s *apiServer) handleUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	writeJSON(w, s.updater.snapshot())
}

func (s *apiServer) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	status, err := s.updater.check(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, status)
}

func (s *apiServer) handleUpdateInstall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	status, err := s.updater.startInstall()
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, status)
}
