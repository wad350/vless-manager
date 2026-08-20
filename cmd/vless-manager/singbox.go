package main

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"strings"
)

const (
	// tunIface / tunAddr / tunMTU describe the TUN device sing-box creates.
	// The system stack uses a /30 so it has both a "server" address (.1)
	// and a "client" NAT address (.2) without wasting space.
	// routing.go references these same constants.
	tunIface = "tun0"
	tunAddr  = "198.18.0.1/30" // .1 = sing-box server, .2 = NAT source
	tunMTU   = 1500

	// socksHealthPort is a SOCKS5 inbound bound to localhost that the failover
	// controller uses to probe VPN health.
	socksHealthPort = 7891
)

// generateSingBoxConfig builds a sing-box config: TUN inbound (system stack)
// + VLESS outbound. LAN traffic arrives at tun0 via iptables fwmark routing
// set up by routing.go. The "system" stack processes packets entirely in
// userspace (no gVisor), handles TCP/UDP NAT, and fakes ICMP echo replies
// so LAN clients' pings appear to succeed.
//
// TUN MTU is intentionally fixed at 1500. The physical LTE/WAN interface
// negotiates its own MTU; this virtual interface carries client IP packets.
func generateSingBoxConfig(cfg *Config, srv *VLESSServer) ([]byte, error) {
	logLevel := cfg.Settings.LogLevel
	if logLevel == "" {
		logLevel = "error"
	}
	// TUN inbound: sing-box creates tun0 and reads raw IP packets.
	// system stack = pure-Go userspace NAT; no iptables internals,
	// no gVisor memory overhead — right for a 57 MB MIPS router.
	// auto_route=false: we manage ip-rule routing ourselves in routing.go.
	inboundTun := map[string]any{
		"type":           "tun",
		"tag":            "tun-in",
		"interface_name": tunIface,
		"address":        []string{tunAddr},
		"mtu":            tunMTU,
		"stack":          "system",
	}

	// socks inbound: localhost-only, used by vpnProbe in failover.go.
	inboundSocks := map[string]any{
		"type":        "socks",
		"tag":         "socks-health",
		"listen":      "127.0.0.1",
		"listen_port": socksHealthPort,
	}

	proxyOutbounds, err := buildSingBoxProxyOutbounds(srv)
	if err != nil {
		return nil, err
	}

	// `direct` outbound must carry the WAN-fwmark too. Without it, sing-box
	// opens raw sockets that the OUTPUT mangle chain re-marks with 0x1 and
	// kicks back into tun0 — domain-bypass would silently loop.
	directOut := map[string]any{"type": "direct", "tag": "direct"}
	if runtime.GOOS == "linux" {
		directOut["routing_mark"] = WANFwmark
	}
	outbounds := append(proxyOutbounds, directOut)

	// The engine's own resolver remains local and uses the router's
	// resolv.conf. Client queries addressed to the router stay local because
	// its LAN address matches the private-CIDR bypass. Queries addressed to a
	// public resolver enter tun0 like any other TCP/UDP traffic.
	dns := map[string]any{
		"servers": []map[string]any{
			{"tag": "dns_local", "type": "local"},
		},
		"final":    "dns_local",
		"strategy": "ipv4_only",
	}

	rules := []map[string]any{
		// Sniff TLS SNI / HTTP Host so domain rules work without a DNS
		// lookup. Cheap, no extra connections.
		{"action": "sniff"},
	}
	// Domain-based bypass list: RU operator whitelist + user additions →
	// `direct`. Placed BEFORE the private-CIDR rule because matching is
	// first-hit. Empty list ⇒ rule is skipped, keeping the config slim
	// when the user disables it.
	if bypass := bypassDomainsFor(cfg); len(bypass) > 0 {
		rules = append(rules, map[string]any{
			"domain_suffix": bypass,
			"outbound":      "direct",
		})
	}
	// Private CIDRs — keep direct (already RETURN'd by mangle chain, but
	// defensive in case anything slips through).
	rules = append(rules, map[string]any{
		"ip_cidr": []string{
			"192.168.0.0/16",
			"10.0.0.0/8",
			"172.16.0.0/12",
			"127.0.0.0/8",
			"169.254.0.0/16",
			"224.0.0.0/4",
		},
		"outbound": "direct",
	})

	route := map[string]any{
		"rules":                   rules,
		"final":                   "proxy",
		"default_domain_resolver": "dns_local",
	}

	config := map[string]any{
		"log": map[string]any{
			"level":     logLevel,
			"timestamp": false,
			"output":    os.DevNull,
		},
		"dns":       dns,
		"inbounds":  []any{inboundTun, inboundSocks},
		"outbounds": outbounds,
		"route":     route,
	}

	return json.MarshalIndent(config, "", "  ")
}

func buildSingBoxProxyOutbounds(srv *VLESSServer) ([]map[string]any, error) {
	if len(srv.Members) == 0 {
		out, err := buildSingBoxVLESSOutbound(srv)
		if err != nil {
			return nil, err
		}
		return []map[string]any{out}, nil
	}

	outbounds := make([]map[string]any, 0, len(srv.Members)+1)
	tags := make([]string, 0, len(srv.Members))
	for i := range srv.Members {
		out, err := buildSingBoxVLESSOutbound(&srv.Members[i])
		if err != nil {
			return nil, fmt.Errorf("profile member %d: %w", i+1, err)
		}
		tag := fmt.Sprintf("proxy-%d", i+1)
		out["tag"] = tag
		tags = append(tags, tag)
		outbounds = append(outbounds, out)
	}
	outbounds = append(outbounds, map[string]any{
		"type":                        "urltest",
		"tag":                         "proxy",
		"outbounds":                   tags,
		"url":                         defaultPingTestURL,
		"interval":                    "10m",
		"tolerance":                   50,
		"idle_timeout":                "30m",
		"interrupt_exist_connections": false,
	})
	return outbounds, nil
}

func buildSingBoxVLESSOutbound(srv *VLESSServer) (map[string]any, error) {
	out := map[string]any{
		"type":        "vless",
		"tag":         "proxy",
		"server":      srv.Address,
		"server_port": srv.Port,
		"uuid":        srv.UUID,
		// VLESS carries UDP over its TCP transport using XUDP. This is also
		// sing-box's current default, but keeping it explicit protects global
		// UDP routing from a future engine-default change.
		"packet_encoding": orDefault(srv.PacketEncoding, "xudp"),
		// A bad CDN/WS edge must fail quickly. Without an explicit cap,
		// browsers can pile up hundreds of pending handshakes on a small
		// router before the periodic health check replaces the server.
		"connect_timeout": "4s",
	}
	if runtime.GOOS == "linux" {
		// Mark sing-box's own outbound sockets so the OUTPUT mangle chain
		// returns them to the main WAN route instead of routing them back
		// into tun0. This is critical for domain/CDN VLESS hosts, where the
		// actual dialed IP may differ from the IPs resolved by routing.go.
		out["routing_mark"] = WANFwmark
	}
	if srv.Flow != "" {
		out["flow"] = srv.Flow
	}
	alpnList := splitCSV(srv.ALPN)
	switch srv.Security {
	case "reality":
		tls := map[string]any{
			"enabled":     true,
			"server_name": srv.SNI,
			"utls": map[string]any{
				"enabled":     true,
				"fingerprint": orDefault(srv.Fingerprint, "chrome"),
			},
			"reality": map[string]any{
				"enabled":    true,
				"public_key": srv.PublicKey,
				"short_id":   srv.ShortID,
			},
		}
		if len(alpnList) > 0 {
			tls["alpn"] = alpnList
		}
		out["tls"] = tls
	case "tls":
		tls := map[string]any{
			"enabled":     true,
			"server_name": srv.SNI,
		}
		if len(alpnList) > 0 {
			tls["alpn"] = alpnList
		}
		if srv.Fingerprint != "" {
			tls["utls"] = map[string]any{
				"enabled":     true,
				"fingerprint": srv.Fingerprint,
			}
		}
		out["tls"] = tls
	}

	switch normalizeVLESSNetwork(srv.Network) {
	case "ws":
		t := map[string]any{"type": "ws"}
		if srv.Path != "" {
			t["path"] = srv.Path
		}
		if srv.Host != "" {
			t["headers"] = map[string]string{"Host": srv.Host}
		}
		out["transport"] = t
	case "grpc":
		out["transport"] = map[string]any{
			"type":         "grpc",
			"service_name": srv.Path,
		}
	case "xhttp":
		out["transport"] = buildSingBoxXHTTPTransport(srv)
	case "h2", "http":
		t := map[string]any{"type": "http"}
		if srv.Host != "" {
			t["host"] = []string{srv.Host}
		}
		if srv.Path != "" {
			t["path"] = srv.Path
		}
		out["transport"] = t
	case "httpupgrade":
		t := map[string]any{"type": "httpupgrade"}
		if srv.Host != "" {
			t["host"] = srv.Host
		}
		if srv.Path != "" {
			t["path"] = srv.Path
		}
		out["transport"] = t
	case "quic":
		out["transport"] = map[string]any{"type": "quic"}
	default:
		if !isSupportedServer(srv) {
			return nil, fmt.Errorf("transport %s is not supported by bundled sing-box %s", srv.Network, BundledSingBox)
		}
	}

	return out, nil
}

func buildSingBoxXHTTPTransport(srv *VLESSServer) map[string]any {
	transport := map[string]any{
		"type":            "xhttp",
		"mode":            orDefault(srv.Mode, "auto"),
		"x_padding_bytes": orDefault(srv.XPadding, "100-1000"),
	}

	// Providers commonly put extended XHTTP fields in the share-link `extra`
	// object. Keep a strict allowlist so unrelated Xray-only keys cannot make
	// the embedded sing-box configuration invalid.
	var extra map[string]any
	if len(srv.Extra) > 0 && json.Unmarshal(srv.Extra, &extra) == nil {
		aliases := map[string]string{
			"mode": "mode", "host": "host", "path": "path", "headers": "headers",
			"domainStrategy": "domain_strategy", "domain_strategy": "domain_strategy",
			"xPaddingBytes": "x_padding_bytes", "x_padding_bytes": "x_padding_bytes",
			"noGRPCHeader": "no_grpc_header", "no_grpc_header": "no_grpc_header",
			"noSSEHeader": "no_sse_header", "no_sse_header": "no_sse_header",
			"scMaxEachPostBytes": "sc_max_each_post_bytes", "sc_max_each_post_bytes": "sc_max_each_post_bytes",
			"scMinPostsIntervalMs": "sc_min_posts_interval_ms", "sc_min_posts_interval_ms": "sc_min_posts_interval_ms",
			"scMaxBufferedPosts": "sc_max_buffered_posts", "sc_max_buffered_posts": "sc_max_buffered_posts",
			"scStreamUpServerSecs": "sc_stream_up_server_secs", "sc_stream_up_server_secs": "sc_stream_up_server_secs",
			"serverMaxHeaderBytes": "server_max_header_bytes", "server_max_header_bytes": "server_max_header_bytes",
			"trustedXForwardedFor": "trusted_x_forwarded_for", "trusted_x_forwarded_for": "trusted_x_forwarded_for",
			"xmux": "xmux", "xPaddingObfsMode": "x_padding_obfs_mode", "x_padding_obfs_mode": "x_padding_obfs_mode",
			"xPaddingKey": "x_padding_key", "x_padding_key": "x_padding_key",
			"xPaddingHeader": "x_padding_header", "x_padding_header": "x_padding_header",
			"xPaddingPlacement": "x_padding_placement", "x_padding_placement": "x_padding_placement",
			"xPaddingMethod": "x_padding_method", "x_padding_method": "x_padding_method",
			"uplinkHTTPMethod": "uplink_http_method", "uplink_http_method": "uplink_http_method",
			"sessionPlacement": "session_placement", "session_placement": "session_placement",
			"sessionIDPlacement": "session_placement", "sessionIdPlacement": "session_placement",
			"sessionKey": "session_key", "session_key": "session_key",
			"sessionIDKey": "session_key", "sessionIdKey": "session_key",
			"seqPlacement": "seq_placement", "seq_placement": "seq_placement",
			"seqKey": "seq_key", "seq_key": "seq_key",
			"uplinkDataPlacement": "uplink_data_placement", "uplink_data_placement": "uplink_data_placement",
			"uplinkDataKey": "uplink_data_key", "uplink_data_key": "uplink_data_key",
			"uplinkChunkSize": "uplink_chunk_size", "uplink_chunk_size": "uplink_chunk_size",
			"sessionIDTable": "session_id_table", "sessionIdTable": "session_id_table", "session_id_table": "session_id_table",
			"sessionIDLength": "session_id_length", "sessionIdLength": "session_id_length", "session_id_length": "session_id_length",
			"congestionController": "congestion_controller", "congestion_controller": "congestion_controller",
			"cwnd": "cwnd", "downloadSettings": "download", "download": "download",
		}
		for source, target := range aliases {
			if value, ok := extra[source]; ok && value != nil {
				transport[target] = normalizeXHTTPValue(value)
			}
		}
	}

	// Dedicated fields are authoritative because explicit share-link query
	// parameters override values inside `extra`.
	setString := func(key, value string) {
		if value != "" {
			transport[key] = value
		}
	}
	setString("mode", srv.Mode)
	setString("host", srv.Host)
	setString("path", srv.Path)
	setString("x_padding_bytes", srv.XPadding)
	setString("session_placement", srv.SessionPlacement)
	setString("session_key", srv.SessionKey)
	setString("seq_placement", srv.SeqPlacement)
	setString("seq_key", srv.SeqKey)
	setString("uplink_http_method", srv.UplinkHTTPMethod)
	setString("uplink_data_placement", srv.UplinkDataPlacement)
	setString("uplink_data_key", srv.UplinkDataKey)
	setString("x_padding_key", srv.XPaddingKey)
	setString("x_padding_header", srv.XPaddingHeader)
	setString("x_padding_placement", srv.XPaddingPlacement)
	setString("x_padding_method", srv.XPaddingMethod)
	if len(srv.XHTTPHeaders) > 0 {
		transport["headers"] = srv.XHTTPHeaders
	}
	if srv.NoSSEHeader {
		transport["no_sse_header"] = true
	}
	if srv.NoGRPCHeader {
		transport["no_grpc_header"] = true
	}
	if srv.XPaddingObfsMode {
		transport["x_padding_obfs_mode"] = true
	}
	if srv.ScMaxBufferedPosts != 0 {
		transport["sc_max_buffered_posts"] = srv.ScMaxBufferedPosts
	}
	if len(srv.Xmux) > 0 {
		var xmux any
		if json.Unmarshal(srv.Xmux, &xmux) == nil {
			transport["xmux"] = xmux
		}
	}
	if len(srv.DownloadSettings) > 0 {
		var download any
		if json.Unmarshal(srv.DownloadSettings, &download) == nil {
			transport["download"] = normalizeXHTTPValue(download)
		}
	}
	ensureXHTTPPadding(transport)
	return transport
}

func ensureXHTTPPadding(options map[string]any) {
	padding, exists := options["x_padding_bytes"]
	invalid := !exists || padding == nil
	if value, ok := padding.(string); ok {
		invalid = strings.TrimSpace(value) == "" || strings.TrimSpace(value) == "0"
	}
	if value, ok := padding.(float64); ok {
		invalid = value <= 0
	}
	if value, ok := padding.(int); ok {
		invalid = value <= 0
	}
	if invalid {
		options["x_padding_bytes"] = "100-1000"
	}
	// Xray subscriptions use zero to mean "use the implementation default".
	// Extended sing-box represents this option as a pointer and rejects an
	// explicitly supplied zero, while an omitted value correctly selects its
	// 1 MB default.
	if isDisabledXHTTPRange(options["sc_max_each_post_bytes"]) {
		delete(options, "sc_max_each_post_bytes")
	}
	if download, ok := options["download"].(map[string]any); ok {
		ensureXHTTPPadding(download)
	}
}

func isDisabledXHTTPRange(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case string:
		value := strings.TrimSpace(typed)
		return value == "" || value == "0" || value == "0-0"
	case float64:
		return typed <= 0
	case float32:
		return typed <= 0
	case int:
		return typed <= 0
	case int32:
		return typed <= 0
	case int64:
		return typed <= 0
	case json.Number:
		number, err := typed.Float64()
		return err == nil && number <= 0
	default:
		return false
	}
}

func normalizeXHTTPValue(value any) any {
	obj, ok := value.(map[string]any)
	if !ok {
		return value
	}
	aliases := map[string]string{
		"domainStrategy": "domain_strategy", "xPaddingBytes": "x_padding_bytes",
		"noGRPCHeader": "no_grpc_header", "noSSEHeader": "no_sse_header",
		"scMaxEachPostBytes": "sc_max_each_post_bytes", "scMinPostsIntervalMs": "sc_min_posts_interval_ms",
		"scMaxBufferedPosts": "sc_max_buffered_posts", "scStreamUpServerSecs": "sc_stream_up_server_secs",
		"serverMaxHeaderBytes": "server_max_header_bytes", "trustedXForwardedFor": "trusted_x_forwarded_for",
		"xPaddingObfsMode": "x_padding_obfs_mode", "xPaddingKey": "x_padding_key",
		"xPaddingHeader": "x_padding_header", "xPaddingPlacement": "x_padding_placement",
		"xPaddingMethod": "x_padding_method", "uplinkHTTPMethod": "uplink_http_method",
		"sessionPlacement": "session_placement", "sessionIDPlacement": "session_placement", "sessionIdPlacement": "session_placement",
		"sessionKey": "session_key", "sessionIDKey": "session_key", "sessionIdKey": "session_key",
		"seqPlacement": "seq_placement", "seqKey": "seq_key",
		"uplinkDataPlacement": "uplink_data_placement", "uplinkDataKey": "uplink_data_key",
		"uplinkChunkSize": "uplink_chunk_size", "sessionIDTable": "session_id_table", "sessionIdTable": "session_id_table",
		"sessionIDLength": "session_id_length", "sessionIdLength": "session_id_length", "congestionController": "congestion_controller",
		"maxConcurrency": "max_concurrency", "maxConnections": "max_connections",
		"cMaxReuseTimes": "c_max_reuse_times", "hMaxRequestTimes": "h_max_request_times",
		"hMaxReusableSecs": "h_max_reusable_secs", "hKeepAlivePeriod": "h_keep_alive_period",
		"downloadSettings": "download", "serverName": "server_name", "serverPort": "server_port",
	}
	normalized := make(map[string]any, len(obj))
	for key, nested := range obj {
		if alias := aliases[key]; alias != "" {
			key = alias
		}
		normalized[key] = normalizeXHTTPValue(nested)
	}
	return normalized
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func camelToSnake(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 4)
	for i, r := range s {
		if i > 0 && r >= 'A' && r <= 'Z' {
			prev := rune(s[i-1])
			if prev < 'A' || prev > 'Z' {
				b.WriteByte('_')
			}
		}
		if r >= 'A' && r <= 'Z' {
			r += 'a' - 'A'
		}
		b.WriteRune(r)
	}
	return b.String()
}

func splitCSV(value string) []string {
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
