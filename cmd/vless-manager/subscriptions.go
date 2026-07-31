package main

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	neturl "net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/proxy"
)

var subscriptionDeviceID = ephemeralSubscriptionDeviceID()

var routerIdentityPaths = []string{
	"/sys/class/net/Bridge0/address",
	"/sys/class/net/br0/address",
	"/sys/class/net/eth0/address",
	"/proc/device-tree/serial-number",
	"/etc/machine-id",
}

func ephemeralSubscriptionDeviceID() string {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err == nil {
		return "vm-" + hex.EncodeToString(random)
	}
	// Production replaces this during startup with a hardware-bound value.
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano())))
	return "vm-" + hex.EncodeToString(sum[:16])
}

// initializeSubscriptionDeviceID assigns a stable pseudonymous X-Hwid for
// providers with per-device limits. The raw MAC or serial never leaves the
// router. The derived value is persisted to survive interface renames.
func initializeSubscriptionDeviceID(dataDir string) error {
	path := filepath.Join(dataDir, "device-hwid")
	if data, err := os.ReadFile(path); err == nil {
		if id := strings.TrimSpace(string(data)); validSubscriptionDeviceID(id) {
			subscriptionDeviceID = id
			return nil
		}
	}

	var identity string
	for _, candidate := range routerIdentityPaths {
		data, err := os.ReadFile(candidate)
		if err != nil {
			continue
		}
		value := strings.Trim(strings.TrimSpace(string(data)), "\x00")
		if value != "" {
			identity = candidate + "\x00" + strings.ToLower(value)
			break
		}
	}
	if identity == "" {
		random := make([]byte, 32)
		if _, err := rand.Read(random); err != nil {
			return fmt.Errorf("generate subscription device ID: %w", err)
		}
		identity = "random\x00" + hex.EncodeToString(random)
	}

	sum := sha256.Sum256([]byte("vless-manager-hwid-v1\x00" + identity))
	subscriptionDeviceID = "vm-" + hex.EncodeToString(sum[:16])

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(subscriptionDeviceID+"\n"), 0600); err != nil {
		return fmt.Errorf("persist subscription device ID: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("persist subscription device ID: %w", err)
	}
	return nil
}

func validSubscriptionDeviceID(id string) bool {
	if !strings.HasPrefix(id, "vm-") || len(id) != 35 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(id, "vm-"))
	return err == nil
}

// subscriptionID returns a stable ID derived from the URL so re-adding the
// same subscription yields the same ID (was nanosecond timestamp before).
func subscriptionID(url string) string {
	h := sha256.Sum256([]byte(url))
	return hex.EncodeToString(h[:8])
}

// SubUserInfo is the parsed `subscription-userinfo` header that VPN providers
// return alongside the server list. Fields default to 0 when not provided.
// Format: "upload=X; download=Y; total=Z; expire=T" (T is Unix timestamp).
type SubUserInfo struct {
	Upload   int64 `json:"upload"`
	Download int64 `json:"download"`
	Total    int64 `json:"total"`
	Expire   int64 `json:"expire"`
}

// Used returns total bytes transferred (upload+download).
func (u *SubUserInfo) Used() int64 { return u.Upload + u.Download }

// Remaining returns bytes still available, or -1 if no quota is set.
func (u *SubUserInfo) Remaining() int64 {
	if u.Total <= 0 {
		return -1
	}
	r := u.Total - u.Used()
	if r < 0 {
		return 0
	}
	return r
}

type Subscription struct {
	ID           string        `json:"id"`
	Name         string        `json:"name"`
	ProviderName string        `json:"provider_name,omitempty"`
	Description  string        `json:"description,omitempty"`
	URL          string        `json:"url"`
	Servers      []VLESSServer `json:"servers"`
	UpdatedAt    time.Time     `json:"updated_at"`
	Error        string        `json:"error,omitempty"`
	UserInfo     *SubUserInfo  `json:"user_info,omitempty"`
	Disabled     bool          `json:"disabled,omitempty"`
	// DisabledServerIDs contains only the user's explicit choices. Ping
	// failures never modify this list.
	DisabledServerIDs  []string       `json:"disabled_server_ids,omitempty"`
	ExcludedServers    int            `json:"excluded_servers,omitempty"`
	ExcludedTransports map[string]int `json:"excluded_transports,omitempty"`
}

func containsServerID(ids []string, id string) bool {
	for _, disabledID := range ids {
		if disabledID == id {
			return true
		}
	}
	return false
}

func setServerIDState(ids []string, id string, present bool) ([]string, bool) {
	next := make([]string, 0, len(ids)+1)
	found := false
	for _, disabledID := range ids {
		if disabledID == id {
			found = true
			if present {
				next = append(next, disabledID)
			}
			continue
		}
		next = append(next, disabledID)
	}
	if present && !found {
		next = append(next, id)
	}
	changed := (present && !found) || (!present && found)
	return next, changed
}

func (s *Subscription) manuallyDisabled(id string) bool {
	return containsServerID(s.DisabledServerIDs, id)
}

func (s *Subscription) serverDisabled(id string) bool {
	return s.manuallyDisabled(id)
}

func (s *Subscription) setServerDisabled(id string, disabled bool) {
	s.DisabledServerIDs, _ = setServerIDState(s.DisabledServerIDs, id, disabled)
}

func preserveSubscriptionOptions(fresh, previous *Subscription) {
	fresh.Disabled = previous.Disabled
	for _, id := range previous.DisabledServerIDs {
		var oldServer *VLESSServer
		for i := range previous.Servers {
			if previous.Servers[i].ID == id {
				oldServer = &previous.Servers[i]
				break
			}
		}
		for _, candidate := range fresh.Servers {
			if candidate.ID == id || (oldServer != nil && sameCatalogServer(*oldServer, candidate)) {
				fresh.DisabledServerIDs = append(fresh.DisabledServerIDs, candidate.ID)
				break
			}
		}
	}
}

func preserveSubscriptionDisplayName(fresh, previous *Subscription) {
	previousName := strings.TrimSpace(previous.Name)
	wasAutomatic := previousName == "" ||
		previousName == strings.TrimSpace(previous.ProviderName) ||
		previousName == subscriptionFallbackName(previous.URL)
	if !wasAutomatic {
		fresh.Name = previousName
	}
}

func serverDisabledInSubscriptions(subs []*Subscription, id string) bool {
	found := false
	for _, sub := range subs {
		for _, server := range sub.Servers {
			if server.ID != id {
				continue
			}
			found = true
			if !sub.Disabled && !sub.serverDisabled(id) {
				return false
			}
		}
	}
	return found
}

func enabledSubscriptionServers(sub *Subscription) []VLESSServer {
	if sub.Disabled {
		return nil
	}
	out := make([]VLESSServer, 0, len(sub.Servers))
	for _, srv := range sub.Servers {
		if !sub.serverDisabled(srv.ID) {
			out = append(out, srv)
		}
	}
	return out
}

func normalizeSubscriptionURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "happ://add/") {
		return strings.TrimPrefix(raw, "happ://add/")
	}
	if strings.HasPrefix(raw, "happ://") {
		u, err := neturl.Parse(raw)
		if err != nil {
			return raw
		}
		if u.Host == "add" {
			if q := u.Query().Get("url"); q != "" {
				return q
			}
			return strings.TrimPrefix(u.Path, "/")
		}
	}
	return raw
}

func fetchSubscription(url string) (*Subscription, error) {
	return fetchSubscriptionWithTimeout(url, 15*time.Second)
}

// fetchSubscriptionWithTimeout is the explicit-timeout variant used by the
// settings-aware refresh loop. Plain fetchSubscription keeps the 15 s default
// for compatibility with the few call sites that don't have a Config handy.
func fetchSubscriptionWithTimeout(url string, timeout time.Duration) (*Subscription, error) {
	return fetchSubscriptionWithClient(url, subscriptionHTTPClient(timeout, nil))
}

func (s *apiServer) fetchSubscriptionURL(url string) (*Subscription, error) {
	return s.fetchSubscriptionURLWithTimeout(url, s.settingsSnapshot().SubscriptionFetchTimeout())
}

func (s *apiServer) fetchSubscriptionURLWithTimeout(url string, timeout time.Duration) (*Subscription, error) {
	st := s.settingsSnapshot()
	vpnState := s.failover.State()
	vpnReady := s.pm.Status().Running && s.pm.TunRunning() && vpnState.VPNHealthOK
	host := "unknown"
	if parsed, err := neturl.Parse(url); err == nil && parsed.Host != "" {
		host = parsed.Host
	}
	opID := s.pm.nextOperationID("subfetch")
	if st.SubscriptionPreferVPN && vpnReady {
		dialer, err := proxy.SOCKS5("tcp", fmt.Sprintf("127.0.0.1:%d", socksHealthPort), nil, proxy.Direct)
		if err == nil {
			s.pm.event(serviceLogDebug, "subscription", "fetch.started",
				"загрузка подписки через рабочий VPN",
				field("op_id", opID),
				field("host", host),
				field("transport", "vpn"),
				field("timeout_ms", timeout.Milliseconds()))
			sub, fetchErr := fetchSubscriptionWithClient(url, subscriptionHTTPClient(timeout, dialer.Dial))
			if fetchErr == nil {
				s.pm.event(serviceLogDebug, "subscription", "fetch.succeeded",
					"подписка загружена",
					field("op_id", opID),
					field("host", host),
					field("transport", "vpn"),
					field("servers", len(sub.Servers)),
					field("excluded", sub.ExcludedServers))
				return sub, nil
			}
			s.pm.event(serviceLogWarn, "subscription", "fetch.fallback",
				"загрузка через VPN не удалась, выполняется повтор через WAN",
				field("op_id", opID),
				field("host", host),
				field("error", fetchErr))
		}
	}

	// Force WAN routing when the main tunnel exists but is not healthy. This
	// keeps the global checkbox honest: an unhealthy TUN is never used.
	s.pm.event(serviceLogDebug, "subscription", "fetch.started",
		"загрузка подписки через WAN",
		field("op_id", opID),
		field("host", host),
		field("transport", "wan"),
		field("timeout_ms", timeout.Milliseconds()))
	sub, err := fetchSubscriptionWithClient(url, subscriptionHTTPClient(timeout, wanDialer(timeout).Dial))
	if err != nil {
		s.pm.event(serviceLogWarn, "subscription", "fetch.failed",
			"подписка не загружена",
			field("op_id", opID),
			field("host", host),
			field("transport", "wan"),
			field("error", err))
		return nil, err
	}
	s.pm.event(serviceLogDebug, "subscription", "fetch.succeeded",
		"подписка загружена",
		field("op_id", opID),
		field("host", host),
		field("transport", "wan"),
		field("servers", len(sub.Servers)),
		field("excluded", sub.ExcludedServers))
	return sub, nil
}

func subscriptionHTTPClient(timeout time.Duration, dial func(string, string) (net.Conn, error)) *http.Client {
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: false},
	}
	if dial != nil {
		transport.Dial = dial
	}
	return &http.Client{Timeout: timeout, Transport: transport}
}

func fetchSubscriptionWithClient(url string, client *http.Client) (*Subscription, error) {
	if subscriptionDeviceID == "" {
		return nil, fmt.Errorf("subscription device ID is not initialized")
	}
	url = normalizeSubscriptionURL(url)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "v2rayN/6.0")
	// Remnawave providers with device limits return placeholder nodes unless
	// the client identifies a stable device. Keep this ID unchanged between
	// updates so the router occupies only one provider device slot.
	req.Header.Set("X-Hwid", subscriptionDeviceID)
	req.Header.Set("X-Device-Os", "Linux")
	req.Header.Set("X-Device-Model", "Keenetic")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("fetch: HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1MB max
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	text := strings.TrimSpace(string(body))

	// Try base64 decode first (standard subscription format)
	if decoded, err := base64Decode(text); err == nil && containsProxyURI(decoded) {
		text = decoded
	}

	metadata := parseSubscriptionMetadata(resp.Header, text)

	var parsedServers []VLESSServer
	placeholderCount := 0
	unsupportedCount := 0
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "vless://") {
			if srv, err := parseVLESSURI(line); err == nil {
				parsedServers = append(parsedServers, *srv)
			}
		}
	}
	if len(parsedServers) == 0 {
		parsedServers = parseXrayJSONServers([]byte(text))
	}

	servers := make([]VLESSServer, 0, len(parsedServers))
	seen := make(map[string]bool, len(parsedServers))
	excludedTransports := make(map[string]int)
	for _, srv := range parsedServers {
		srv.ID = serverFingerprint(srv)
		if seen[srv.ID] {
			continue
		}
		seen[srv.ID] = true
		if isPlaceholderServer(&srv) {
			placeholderCount++
			continue
		}
		if !isSupportedServer(&srv) {
			unsupportedCount++
			excludedTransports[normalizeVLESSNetwork(srv.Network)]++
			continue
		}
		servers = append(servers, srv)
	}

	if len(servers) == 0 {
		if unsupportedCount > 0 {
			return nil, fmt.Errorf("subscription contains no transports supported by sing-box %s (%d excluded)", BundledSingBox, unsupportedCount)
		}
		if placeholderCount > 0 {
			return nil, fmt.Errorf("subscription returned only placeholder nodes; provider rejected this client/app")
		}
		return nil, fmt.Errorf("no VLESS servers found in subscription (got %d bytes)", len(body))
	}

	sub := &Subscription{
		ID:                 subscriptionID(url),
		ProviderName:       metadata.Name,
		Description:        metadata.Description,
		URL:                url,
		Servers:            servers,
		UpdatedAt:          time.Now(),
		UserInfo:           metadata.UserInfo,
		ExcludedServers:    unsupportedCount,
		ExcludedTransports: excludedTransports,
	}
	if metadata.Name != "" {
		sub.Name = metadata.Name
	} else {
		sub.Name = subscriptionFallbackName(url)
	}
	return sub, nil
}

type subscriptionMetadata struct {
	Name        string
	Description string
	UserInfo    *SubUserInfo
}

func parseSubscriptionMetadata(header http.Header, body string) subscriptionMetadata {
	bodyValues := make(map[string]string)
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(strings.TrimSpace(strings.TrimPrefix(line, "#")), ":")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		if _, exists := bodyValues[key]; !exists {
			bodyValues[key] = strings.TrimSpace(value)
		}
	}

	value := func(key string) string {
		if headerValue := strings.TrimSpace(header.Get(key)); headerValue != "" {
			return headerValue
		}
		return bodyValues[strings.ToLower(key)]
	}

	return subscriptionMetadata{
		Name:        limitMetadataText(decodeSubscriptionMetadataValue(value("profile-title")), 256),
		Description: limitMetadataText(decodeSubscriptionMetadataValue(value("announce")), 4096),
		UserInfo:    parseSubUserInfo(value("subscription-userinfo")),
	}
}

func decodeSubscriptionMetadataValue(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(value), "base64:") {
		encoded := strings.TrimSpace(value[len("base64:"):])
		for _, encoding := range []*base64.Encoding{
			base64.StdEncoding,
			base64.RawStdEncoding,
			base64.URLEncoding,
			base64.RawURLEncoding,
		} {
			if decoded, err := encoding.DecodeString(encoded); err == nil {
				return strings.TrimSpace(string(decoded))
			}
		}
		return ""
	}
	if decoded, err := neturl.QueryUnescape(value); err == nil {
		value = decoded
	}
	return strings.TrimSpace(value)
}

func limitMetadataText(value string, maxRunes int) string {
	runes := []rune(strings.ReplaceAll(value, "\x00", ""))
	if len(runes) > maxRunes {
		runes = runes[:maxRunes]
	}
	return strings.TrimSpace(string(runes))
}

func subscriptionFallbackName(rawURL string) string {
	parsed, err := neturl.Parse(rawURL)
	if err == nil && parsed.Hostname() != "" {
		return parsed.Hostname()
	}
	return rawURL
}

type xraySubscriptionConfig struct {
	Remarks   string         `json:"remarks"`
	Outbounds []xrayOutbound `json:"outbounds"`
}

type xrayOutbound struct {
	Protocol string `json:"protocol"`
	Tag      string `json:"tag"`
	Settings struct {
		VNext []struct {
			Address string `json:"address"`
			Port    int    `json:"port"`
			Users   []struct {
				ID         string `json:"id"`
				Flow       string `json:"flow"`
				Encryption string `json:"encryption"`
			} `json:"users"`
		} `json:"vnext"`
	} `json:"settings"`
	StreamSettings struct {
		Network  string `json:"network"`
		Security string `json:"security"`
		Reality  struct {
			ServerName  string `json:"serverName"`
			Fingerprint string `json:"fingerprint"`
			PublicKey   string `json:"publicKey"`
			ShortID     string `json:"shortId"`
			SpiderX     string `json:"spiderX"`
		} `json:"realitySettings"`
		TLS struct {
			ServerName  string   `json:"serverName"`
			Fingerprint string   `json:"fingerprint"`
			ALPN        []string `json:"alpn"`
		} `json:"tlsSettings"`
		WS struct {
			Path    string         `json:"path"`
			Headers map[string]any `json:"headers"`
		} `json:"wsSettings"`
		GRPC struct {
			ServiceName string `json:"serviceName"`
		} `json:"grpcSettings"`
		HTTP struct {
			Path string   `json:"path"`
			Host []string `json:"host"`
		} `json:"httpSettings"`
		XHTTP struct {
			Path  string          `json:"path"`
			Host  json.RawMessage `json:"host"`
			Mode  string          `json:"mode"`
			Extra json.RawMessage `json:"extra"`
		} `json:"xhttpSettings"`
	} `json:"streamSettings"`
}

// parseXrayJSONServers accepts the subscription shape used by Xray clients:
// either one complete config object or an array of configs. Each config can
// contain several VLESS outbounds (auto-selection and whitelist profiles).
func parseXrayJSONServers(body []byte) []VLESSServer {
	var configs []xraySubscriptionConfig
	if err := json.Unmarshal(body, &configs); err != nil {
		var single xraySubscriptionConfig
		if err := json.Unmarshal(body, &single); err != nil || len(single.Outbounds) == 0 {
			return nil
		}
		configs = []xraySubscriptionConfig{single}
	}

	// Parse single-node profiles first. Auto profiles often repeat those same
	// endpoints; fingerprint de-duplication then retains the useful country
	// name instead of a generated outbound tag.
	servers := make([]VLESSServer, 0, len(configs))
	for _, singleOnly := range []bool{true, false} {
		for _, cfg := range configs {
			vlessCount := 0
			for _, outbound := range cfg.Outbounds {
				if strings.EqualFold(outbound.Protocol, "vless") {
					vlessCount++
				}
			}
			if (vlessCount == 1) != singleOnly {
				continue
			}
			for _, outbound := range cfg.Outbounds {
				if !strings.EqualFold(outbound.Protocol, "vless") {
					continue
				}
				servers = append(servers, xrayOutboundServers(cfg.Remarks, vlessCount, outbound)...)
			}
		}
	}
	return servers
}

func xrayOutboundServers(remarks string, profileVLESSCount int, outbound xrayOutbound) []VLESSServer {
	stream := outbound.StreamSettings
	network := normalizeVLESSNetwork(stream.Network)
	security := strings.ToLower(strings.TrimSpace(stream.Security))
	if security == "" {
		security = "none"
	}
	name := strings.TrimSpace(remarks)
	if profileVLESSCount > 1 && outbound.Tag != "" {
		name = strings.TrimSpace(name + " · " + outbound.Tag)
	}

	var servers []VLESSServer
	for _, target := range outbound.Settings.VNext {
		for _, user := range target.Users {
			if target.Address == "" || target.Port <= 0 || user.ID == "" {
				continue
			}
			serverName := name
			if serverName == "" {
				serverName = target.Address
			}
			srv := VLESSServer{
				Name:        serverName,
				Address:     target.Address,
				Port:        target.Port,
				UUID:        user.ID,
				Flow:        user.Flow,
				Security:    security,
				SNI:         firstNonEmpty(stream.Reality.ServerName, stream.TLS.ServerName),
				Fingerprint: firstNonEmpty(stream.Reality.Fingerprint, stream.TLS.Fingerprint, "chrome"),
				PublicKey:   stream.Reality.PublicKey,
				ShortID:     stream.Reality.ShortID,
				SpiderX:     firstNonEmpty(stream.Reality.SpiderX, "/"),
				Network:     network,
				ALPN:        strings.Join(stream.TLS.ALPN, ","),
			}
			switch network {
			case "ws":
				srv.Path = stream.WS.Path
				if host, ok := stream.WS.Headers["Host"].(string); ok {
					srv.Host = host
				} else if host, ok := stream.WS.Headers["host"].(string); ok {
					srv.Host = host
				}
			case "grpc":
				srv.Path = stream.GRPC.ServiceName
			case "http", "h2":
				srv.Path = stream.HTTP.Path
				if len(stream.HTTP.Host) > 0 {
					srv.Host = stream.HTTP.Host[0]
				}
			case "xhttp":
				srv.Path = stream.XHTTP.Path
				srv.Host = rawJSONFirstString(stream.XHTTP.Host)
				srv.Mode = stream.XHTTP.Mode
				srv.Extra = append(json.RawMessage(nil), stream.XHTTP.Extra...)
			}
			srv.ID = serverFingerprint(srv)
			servers = append(servers, srv)
		}
	}
	return servers
}

func rawJSONFirstString(raw json.RawMessage) string {
	var value string
	if json.Unmarshal(raw, &value) == nil {
		return value
	}
	var values []string
	if json.Unmarshal(raw, &values) == nil && len(values) > 0 {
		return values[0]
	}
	return ""
}

func httpClientViaVLESS(srv *VLESSServer, timeout, startupWait time.Duration, logs *ringBuffer) (*http.Client, boxHandle, error) {
	box, port, err := startTemporaryVLESSSOCKS(srv, logs)
	if err != nil {
		return nil, nil, err
	}
	if startupWait <= 0 {
		startupWait = 300 * time.Millisecond
	}
	time.Sleep(startupWait)
	dialer, err := proxy.SOCKS5("tcp", fmt.Sprintf("127.0.0.1:%d", port), nil, proxy.Direct)
	if err != nil {
		_ = box.Close()
		return nil, nil, err
	}
	return subscriptionHTTPClient(timeout, dialer.Dial), box, nil
}

func isPlaceholderServer(srv *VLESSServer) bool {
	return srv.Address == "0.0.0.0" ||
		srv.Address == "localhost" ||
		srv.Address == "127.0.0.1" ||
		srv.Port <= 1 ||
		srv.UUID == "00000000-0000-0000-0000-000000000000" ||
		strings.Contains(strings.ToLower(srv.Name), "не поддерживается")
}

// base64Decode handles both standard and URL-safe base64, with or without padding.
func base64Decode(s string) (string, error) {
	s = strings.ReplaceAll(s, "\n", "")
	s = strings.ReplaceAll(s, " ", "")
	// Try URL-safe first, then standard
	for _, enc := range []*base64.Encoding{base64.URLEncoding, base64.StdEncoding, base64.RawURLEncoding, base64.RawStdEncoding} {
		if b, err := enc.DecodeString(s); err == nil {
			return string(b), nil
		}
	}
	return "", fmt.Errorf("not base64")
}

func containsProxyURI(s string) bool {
	return strings.Contains(s, "vless://") ||
		strings.Contains(s, "vmess://") ||
		strings.Contains(s, "trojan://") ||
		strings.Contains(s, "ss://")
}

func loadSubscriptions(path string) ([]*Subscription, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var subs []*Subscription
	if err := json.Unmarshal(data, &subs); err != nil {
		return nil, err
	}
	// Migrate stale (nano-timestamp) server IDs inside each subscription, and
	// upgrade subscription IDs to stable URL-hash form for re-imports.
	seenSubID := map[string]bool{}
	uniqueSubs := make([]*Subscription, 0, len(subs))
	for _, sub := range subs {
		sub.URL = normalizeSubscriptionURL(sub.URL)
		sub.ID = subscriptionID(sub.URL)
		if seenSubID[sub.ID] {
			continue // drop duplicates introduced by old timestamp IDs
		}
		seenSubID[sub.ID] = true

		seen := map[string]bool{}
		oldToNew := make(map[string][]string, len(sub.Servers))
		unique := make([]VLESSServer, 0, len(sub.Servers))
		removedUnsupported := 0
		for _, srv := range sub.Servers {
			oldID := srv.ID
			srv.Network = normalizeVLESSNetwork(srv.Network)
			srv.ID = serverFingerprint(srv)
			if oldID != "" {
				oldToNew[oldID] = append(oldToNew[oldID], srv.ID)
			}
			if !isSupportedServer(&srv) {
				removedUnsupported++
				continue
			}
			if seen[srv.ID] {
				continue
			}
			seen[srv.ID] = true
			unique = append(unique, srv)
		}
		sub.Servers = unique
		sub.ExcludedServers += removedUnsupported
		migrateDisabled := func(ids []string) []string {
			disabledSeen := make(map[string]bool)
			disabled := make([]string, 0, len(ids))
			for _, id := range ids {
				migratedIDs := []string{id}
				if migrated, ok := oldToNew[id]; ok {
					migratedIDs = migrated
				}
				for _, migratedID := range migratedIDs {
					if seen[migratedID] && !disabledSeen[migratedID] {
						disabledSeen[migratedID] = true
						disabled = append(disabled, migratedID)
					}
				}
			}
			return disabled
		}
		sub.DisabledServerIDs = migrateDisabled(sub.DisabledServerIDs)
		uniqueSubs = append(uniqueSubs, sub)
	}
	return uniqueSubs, nil
}

// parseSubUserInfo decodes a "subscription-userinfo" HTTP header like:
//
//	"upload=455727232; download=2480269568; total=53687091200; expire=1689523200"
//
// Returns nil if the header is empty or fully unparseable (no recognized keys).
func parseSubUserInfo(header string) *SubUserInfo {
	header = strings.TrimSpace(header)
	if header == "" {
		return nil
	}
	info := &SubUserInfo{}
	found := false
	for _, part := range strings.Split(header, ";") {
		part = strings.TrimSpace(part)
		eq := strings.IndexByte(part, '=')
		if eq <= 0 {
			continue
		}
		key := strings.TrimSpace(part[:eq])
		val := strings.TrimSpace(part[eq+1:])
		n, err := strconv.ParseInt(val, 10, 64)
		if err != nil {
			continue
		}
		switch strings.ToLower(key) {
		case "upload":
			info.Upload = n
			found = true
		case "download":
			info.Download = n
			found = true
		case "total":
			info.Total = n
			found = true
		case "expire":
			info.Expire = n
			found = true
		}
	}
	if !found {
		return nil
	}
	return info
}

func saveSubscriptions(path string, subs []*Subscription) error {
	return writeJSONAtomic(path, subs)
}
