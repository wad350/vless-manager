package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

type VLESSServer struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Manual         bool   `json:"manual,omitempty"`
	Address        string `json:"address"`
	Port           int    `json:"port"`
	UUID           string `json:"uuid"`
	Flow           string `json:"flow"`                      // xtls-rprx-vision or ""
	PacketEncoding string `json:"packet_encoding,omitempty"` // xudp, packetaddr or empty
	Security       string `json:"security"`                  // reality, tls, none
	SNI            string `json:"sni"`
	Fingerprint    string `json:"fingerprint"` // chrome, firefox, safari, randomized
	PublicKey      string `json:"public_key"`  // Reality
	ShortID        string `json:"short_id"`    // Reality
	SpiderX        string `json:"spider_x"`
	Network        string `json:"network"` // tcp, ws, grpc, h2/http, httpupgrade, xhttp
	Path           string `json:"path,omitempty"`
	Host           string `json:"host,omitempty"`
	Mode           string `json:"mode,omitempty"`            // XHTTP mode: auto, packet-up, stream-up, stream-one
	XPadding       string `json:"x_padding_bytes,omitempty"` // XHTTP padding range, e.g. 100-1000
	ALPN           string `json:"alpn,omitempty"`            // comma-separated TLS ALPN list from share URI

	NoSSEHeader         bool   `json:"no_sse_header,omitempty"`
	NoGRPCHeader        bool   `json:"no_grpc_header,omitempty"`
	SessionPlacement    string `json:"session_placement,omitempty"`
	SessionKey          string `json:"session_key,omitempty"`
	SeqPlacement        string `json:"seq_placement,omitempty"`
	SeqKey              string `json:"seq_key,omitempty"`
	UplinkHTTPMethod    string `json:"uplink_http_method,omitempty"`
	UplinkDataPlacement string `json:"uplink_data_placement,omitempty"`
	UplinkDataKey       string `json:"uplink_data_key,omitempty"`
	XPaddingObfsMode    bool   `json:"x_padding_obfs_mode,omitempty"`
	XPaddingKey         string `json:"x_padding_key,omitempty"`
	XPaddingHeader      string `json:"x_padding_header,omitempty"`
	XPaddingPlacement   string `json:"x_padding_placement,omitempty"`
	XPaddingMethod      string `json:"x_padding_method,omitempty"`
	// Xmux is stored as raw JSON so numeric fields (h_keep_alive_period etc.)
	// survive a round-trip without being stringified.
	Xmux               json.RawMessage   `json:"xmux,omitempty"`
	XHTTPHeaders       map[string]string `json:"xhttp_headers,omitempty"`
	ScMaxBufferedPosts int64             `json:"sc_max_buffered_posts,omitempty"`
	// DownloadSettings carries the xhttp "split" download branch (sing-box
	// V2RayXHTTPDownloadOptions). Stored verbatim from share-link `extra`.
	DownloadSettings json.RawMessage `json:"download_settings,omitempty"`
	// Extra is the entire `extra={...}` blob from the share link, kept
	// verbatim. singbox.go re-applies every recognised key into the xhttp
	// transport at build time so newly introduced Xray fields (xPaddingKey,
	// uplinkHTTPMethod=PUT, sc*Posts, custom xmux ranges, …) survive without
	// us having to enumerate them by hand.
	Extra json.RawMessage `json:"extra,omitempty"`

	// Members turns this catalog entry into a logical provider profile. Xray
	// subscriptions commonly publish one profile backed by several VLESS
	// outbounds and a least-load balancer. At runtime it maps to sing-box
	// urltest; leaf entries keep this field empty.
	Members []VLESSServer `json:"members,omitempty"`
}

type Config struct {
	Port         int           `json:"port"` // WebUI/API port
	ActiveServer string        `json:"active_server"`
	Servers      []VLESSServer `json:"servers"`
	Autostart    bool          `json:"autostart"`     // legacy: start VPN at boot if active_server is set
	AutoFailover bool          `json:"auto_failover"` // auto-toggle VPN based on probe results
	// AutoTunnelFailover validates any running VPN, including one started
	// manually, and replaces its server after sustained health failures.
	AutoTunnelFailover bool        `json:"auto_tunnel_failover"`
	Settings           AppSettings `json:"settings"`
	BypassCache        BypassCache `json:"bypass_cache,omitempty"`
}

// BypassCache is a router-local copy of the upstream RU domain whitelist.
// An empty cache transparently falls back to the list embedded in the binary.
type BypassCache struct {
	Domains   []string  `json:"domains,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
	Source    string    `json:"source,omitempty"`
}

func cloneVLESSServer(src VLESSServer) VLESSServer {
	dst := src
	dst.Xmux = append(json.RawMessage(nil), src.Xmux...)
	dst.DownloadSettings = append(json.RawMessage(nil), src.DownloadSettings...)
	dst.Extra = append(json.RawMessage(nil), src.Extra...)
	if src.XHTTPHeaders != nil {
		dst.XHTTPHeaders = make(map[string]string, len(src.XHTTPHeaders))
		for key, value := range src.XHTTPHeaders {
			dst.XHTTPHeaders[key] = value
		}
	}
	if src.Members != nil {
		dst.Members = make([]VLESSServer, len(src.Members))
		for i := range src.Members {
			dst.Members[i] = cloneVLESSServer(src.Members[i])
		}
	}
	return dst
}

func cloneConfig(src *Config) *Config {
	if src == nil {
		return nil
	}
	dst := *src
	dst.Servers = make([]VLESSServer, len(src.Servers))
	for i := range src.Servers {
		dst.Servers[i] = cloneVLESSServer(src.Servers[i])
	}
	dst.Settings.OpenProbes = append([]string(nil), src.Settings.OpenProbes...)
	dst.Settings.WhitelistProbes = append([]string(nil), src.Settings.WhitelistProbes...)
	dst.Settings.BypassDomains = append([]string(nil), src.Settings.BypassDomains...)
	dst.BypassCache.Domains = append([]string(nil), src.BypassCache.Domains...)
	return &dst
}

func defaultConfig() *Config {
	return &Config{
		Port:               3001,
		Servers:            []VLESSServer{},
		Autostart:          true,
		AutoFailover:       true,
		AutoTunnelFailover: true,
		Settings:           defaultSettings(),
	}
}

func loadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return defaultConfig(), nil
	}
	if err != nil {
		return nil, err
	}
	cfg := defaultConfig()
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, err
	}
	if cfg.Servers == nil {
		cfg.Servers = []VLESSServer{}
	}
	// Patch in defaults for any new AppSettings field that was added after
	// the on-disk config was first written.
	cfg.Settings.fillDefaults()
	migrateServerIDs(cfg)
	return cfg, nil
}

// resyncServersFromSubs overwrites manual cfg.Servers entries with fresh
// copies from subscriptions whenever the share-link parser produced something
// different (added xhttp `extra` knobs, fixed an SNI, rotated a UUID, …).
// Returns the number of replaced entries so callers know whether to save.
//
// We compare-and-replace by the complete connection-profile fingerprint, so
// profiles with shared endpoints but different SNI/Reality data stay distinct.
func resyncServersFromSubs(cfg *Config, subs []*Subscription) int {
	idx := make(map[string]*VLESSServer, len(cfg.Servers))
	for i := range cfg.Servers {
		idx[cfg.Servers[i].ID] = &cfg.Servers[i]
	}
	changed := 0
	for _, sub := range subs {
		for _, fresh := range sub.Servers {
			existing, ok := idx[fresh.ID]
			if !ok {
				continue
			}
			// Cheap diff via JSON marshalling — guards against churn from
			// pointer-equal-but-content-equal updates that happen on every
			// refresh tick.
			prev, _ := json.Marshal(existing)
			next, _ := json.Marshal(fresh)
			if string(prev) == string(next) {
				continue
			}
			*existing = fresh
			changed++
		}
	}
	return changed
}

// pruneStaleServers removes cached subscription entries from cfg.Servers when
// their source subscription no longer contains them. Explicit manual servers
// are retained. An active selection is cleared when its cached entry is pruned.
func pruneStaleServers(cfg *Config, subs []*Subscription) int {
	return pruneStaleServersWithActivePolicy(cfg, subs, false)
}

// pruneStaleServersPreservingActive is used while refreshing subscriptions.
// A provider may transiently omit or alter a profile, but that must not tear
// down an already working tunnel. The cached active profile remains usable
// until the tunnel health-check selects a replacement or the user deletes it.
func pruneStaleServersPreservingActive(cfg *Config, subs []*Subscription) int {
	return pruneStaleServersWithActivePolicy(cfg, subs, true)
}

func pruneStaleServersWithActivePolicy(cfg *Config, subs []*Subscription, preserveActive bool) int {
	keep := make(map[string]bool)
	for _, sub := range subs {
		for _, srv := range sub.Servers {
			keep[srv.ID] = true
		}
	}
	out := make([]VLESSServer, 0, len(cfg.Servers))
	pruned := 0
	for _, srv := range cfg.Servers {
		if srv.Manual || keep[srv.ID] || (preserveActive && cfg.ActiveServer == srv.ID) {
			out = append(out, srv)
		} else {
			if cfg.ActiveServer == srv.ID {
				cfg.ActiveServer = ""
			}
			pruned++
		}
	}
	cfg.Servers = out
	return pruned
}

// pruneUnsupportedServers removes nodes that the bundled upstream sing-box
// cannot instantiate. This also clears an incompatible active selection so
// autostart/failover can choose from the supported set.
func pruneUnsupportedServers(cfg *Config) int {
	out := make([]VLESSServer, 0, len(cfg.Servers))
	pruned := 0
	for _, srv := range cfg.Servers {
		srv.Network = normalizeVLESSNetwork(srv.Network)
		if !isSupportedServer(&srv) {
			if cfg.ActiveServer == srv.ID {
				cfg.ActiveServer = ""
			}
			pruned++
			continue
		}
		out = append(out, srv)
	}
	cfg.Servers = out
	return pruned
}

// migrateServerIDs rewrites every server's ID to the deterministic fingerprint
// of its complete connection profile and keeps the active-server pointer valid.
func migrateServerIDs(cfg *Config) {
	oldToNew := make(map[string]string, len(cfg.Servers))
	seen := make(map[string]bool)
	unique := make([]VLESSServer, 0, len(cfg.Servers))
	for _, s := range cfg.Servers {
		s.Network = normalizeVLESSNetwork(s.Network)
		newID := serverFingerprint(s)
		if s.ID != "" {
			oldToNew[s.ID] = newID
		}
		if seen[newID] {
			continue
		}
		seen[newID] = true
		s.ID = newID
		unique = append(unique, s)
	}
	cfg.Servers = unique
	if newID, ok := oldToNew[cfg.ActiveServer]; ok {
		cfg.ActiveServer = newID
	}
}

func saveConfig(path string, cfg *Config) error {
	return writeJSONAtomic(path, cfg)
}

func writeJSONAtomic(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0644); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func (cfg *Config) findServer(id string) *VLESSServer {
	for i := range cfg.Servers {
		if cfg.Servers[i].ID == id {
			return &cfg.Servers[i]
		}
	}
	return nil
}

func (cfg *Config) activeServer() *VLESSServer {
	return cfg.findServer(cfg.ActiveServer)
}
