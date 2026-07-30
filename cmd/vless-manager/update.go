package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"debug/elf"
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
	updateCheckTimeout = 20 * time.Second
	updateFetchTimeout = 5 * time.Minute
	maxUpdateBytes     = 64 << 20
)

var versionPattern = regexp.MustCompile(`^v?([0-9]+)\.([0-9]+)\.([0-9]+)$`)

type UpdateStatus struct {
	CurrentVersion  string     `json:"current_version"`
	LatestVersion   string     `json:"latest_version,omitempty"`
	TargetVersion   string     `json:"target_version,omitempty"`
	Available       bool       `json:"available"`
	State           string     `json:"state"`
	Message         string     `json:"message,omitempty"`
	Progress        int        `json:"progress"`
	DownloadedBytes int64      `json:"downloaded_bytes,omitempty"`
	TotalBytes      int64      `json:"total_bytes,omitempty"`
	BytesPerSecond  int64      `json:"bytes_per_second,omitempty"`
	Transport       string     `json:"transport,omitempty"`
	CheckedAt       *time.Time `json:"checked_at,omitempty"`
	StartedAt       *time.Time `json:"started_at,omitempty"`
	UpdatedAt       *time.Time `json:"updated_at,omitempty"`
	ReleaseURL      string     `json:"release_url,omitempty"`
	Error           string     `json:"error,omitempty"`
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
}

func newAppUpdater(api *apiServer) *appUpdater {
	return &appUpdater{
		api: api,
		status: UpdateStatus{
			CurrentVersion: Version,
			State:          "idle",
		},
	}
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
	if !u.begin("checking") {
		return u.snapshot(), errors.New("проверка обновления уже выполняется")
	}

	release, transport, err := u.fetchRelease(ctx, updateCheckTimeout)
	if err != nil {
		status := UpdateStatus{CurrentVersion: Version, State: "error", Error: err.Error()}
		u.finish(status)
		return status, err
	}
	latest, err := normalizedVersion(release.TagName)
	if err != nil {
		status := UpdateStatus{CurrentVersion: Version, State: "error", Transport: transport, Error: err.Error()}
		u.finish(status)
		return status, err
	}
	available, err := newerVersion(latest, Version)
	if err != nil && Version != "dev" {
		status := UpdateStatus{CurrentVersion: Version, State: "error", Transport: transport, Error: err.Error()}
		u.finish(status)
		return status, err
	}
	checkedAt := time.Now()
	status := UpdateStatus{
		CurrentVersion: Version,
		LatestVersion:  latest,
		Available:      available || Version == "dev",
		State:          "ready",
		Transport:      transport,
		CheckedAt:      &checkedAt,
		ReleaseURL:     release.HTMLURL,
	}
	u.finish(status)
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
	return u.installStarted(ctx)
}

func (u *appUpdater) startInstall() (UpdateStatus, error) {
	if !u.begin("checking") {
		return u.snapshot(), errors.New("обновление уже выполняется")
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), updateFetchTimeout)
		defer cancel()
		_, _ = u.installStarted(ctx)
	}()
	return u.snapshot(), nil
}

func (u *appUpdater) installStarted(ctx context.Context) (UpdateStatus, error) {
	u.setInstallProgress("checking", updateStateMessage("checking"), 4, 0, 0, 0)

	executable, err := os.Executable()
	if err != nil {
		return u.failInstall(err)
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return u.failInstall(err)
	}
	updatePath := executable + ".update"

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
		binary, checksum, assetErr := releaseAssets(release, latest)
		if assetErr != nil {
			return assetErr
		}
		return downloadVerifiedBinary(ctx, client, binary, checksum, updatePath,
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
	if err := scheduleBinaryUpdate(executable, updatePath); err != nil {
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
	binaryName := "vless-manager_" + version + "_linux_mipsle_softfloat"
	checksumName := binaryName + ".sha256"
	var binary, checksum releaseAsset
	for _, asset := range release.Assets {
		candidate := releaseAsset{Name: asset.Name, URL: asset.BrowserDownloadURL, Size: asset.Size}
		switch asset.Name {
		case binaryName:
			binary = candidate
		case checksumName:
			checksum = candidate
		}
	}
	if binary.URL == "" || checksum.URL == "" {
		return releaseAsset{}, releaseAsset{}, fmt.Errorf(
			"в релизе %s отсутствуют %s или его SHA-256", release.TagName, binaryName)
	}
	if binary.Size <= 0 || binary.Size > maxUpdateBytes {
		return releaseAsset{}, releaseAsset{}, fmt.Errorf("недопустимый размер обновления: %d", binary.Size)
	}
	if err := validateGitHubAssetURL(binary.URL); err != nil {
		return releaseAsset{}, releaseAsset{}, err
	}
	if err := validateGitHubAssetURL(checksum.URL); err != nil {
		return releaseAsset{}, releaseAsset{}, err
	}
	return binary, checksum, nil
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

func downloadVerifiedBinary(
	ctx context.Context,
	client *http.Client,
	binary releaseAsset,
	checksum releaseAsset,
	destination string,
	progress func(phase string, downloaded, total int64),
) error {
	if progress != nil {
		progress("checksum", 0, binary.Size)
	}
	expected, err := fetchChecksum(ctx, client, checksum.URL)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, binary.URL, nil)
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
		progress("downloading", 0, binary.Size)
	}
	writer := &updateProgressWriter{
		writer: io.MultiWriter(file, hash),
		total:  binary.Size,
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
		progress("verifying", written, binary.Size)
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if !strings.EqualFold(actual, expected) {
		_ = os.Remove(tmp)
		return errors.New("SHA-256 обновления не совпадает")
	}
	if err := validateMIPSELF(tmp); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if progress != nil {
		progress("preparing", written, binary.Size)
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

func validateMIPSELF(path string) error {
	file, err := elf.Open(path)
	if err != nil {
		return fmt.Errorf("обновление не является ELF-файлом: %w", err)
	}
	defer file.Close()
	if file.Machine != elf.EM_MIPS || file.Class != elf.ELFCLASS32 || file.Data != elf.ELFDATA2LSB {
		return fmt.Errorf("неподходящая архитектура обновления: machine=%s class=%s data=%s",
			file.Machine, file.Class, file.Data)
	}
	return nil
}

func scheduleBinaryUpdate(executable, updatePath string) error {
	initScript := "/opt/etc/init.d/S99vless-manager"
	if _, err := os.Stat(initScript); err != nil {
		return fmt.Errorf("init-скрипт не найден: %w", err)
	}
	scriptPath := "/tmp/vless-manager-apply-update.sh"
	logPath := "/opt/var/log/vless-manager-update.log"
	script := fmt.Sprintf(`#!/bin/sh
sleep 2
BIN=%s
NEW=%s
INIT=%s
LOG=%s
BACKUP="${BIN}.previous"
"$INIT" stop >>"$LOG" 2>&1
cp -p "$BIN" "$BACKUP" >>"$LOG" 2>&1
if mv -f "$NEW" "$BIN" && chmod 755 "$BIN"; then
  "$INIT" start >>"$LOG" 2>&1
  sleep 4
  if "$INIT" status >>"$LOG" 2>&1; then
    rm -f "$BACKUP" "$0"
    exit 0
  fi
fi
mv -f "$BACKUP" "$BIN"
chmod 755 "$BIN"
"$INIT" start >>"$LOG" 2>&1
rm -f "$NEW" "$0"
exit 1
`, shellQuote(executable), shellQuote(updatePath), shellQuote(initScript), shellQuote(logPath))
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
