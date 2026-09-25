package main

import (
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
)

const (
	tunIface        = "tun0"
	tunAddr         = "198.18.0.1/30"
	tunMTU          = 1500
	socksHealthPort = 7891
)

func generateXrayConfig(cfg *Config, srv *VLESSServer) ([]byte, error) {
	config, err := xrayBaseConfig(srv)
	if err != nil {
		return nil, err
	}
	config["log"] = map[string]any{"loglevel": xrayLogLevel(cfg.Settings.LogLevel), "access": "none"}
	config["inbounds"] = []any{
		map[string]any{
			"protocol": "tun", "tag": "tun-in",
			"settings": map[string]any{"name": tunIface, "mtu": tunMTU, "gateway": []string{tunAddr}},
			"sniffing": map[string]any{"enabled": true, "destOverride": []string{"http", "tls", "quic"}, "routeOnly": true},
		},
		xraySOCKSInbound(socksHealthPort),
	}
	// Client DNS to the router remains local. Public TCP/UDP DNS follows the
	// same proxy policy as other traffic, with no DNS interception or FakeIP.
	routing := config["routing"].(map[string]any)
	rules := []map[string]any{}
	if bypass := bypassDomainsFor(cfg); len(bypass) > 0 {
		domains := make([]string, 0, len(bypass))
		for _, domain := range bypass {
			domains = append(domains, "domain:"+domain)
		}
		rules = append(rules, map[string]any{"type": "field", "domain": domains, "outboundTag": "direct"})
	}
	rules = append(rules, map[string]any{"type": "field", "ip": privateCIDRs, "outboundTag": "direct"})
	routing["rules"] = append(rules, routing["rules"].([]map[string]any)...)
	return json.MarshalIndent(config, "", "  ")
}

func xrayLogLevel(level string) string {
	switch level {
	case "trace", "debug":
		return "debug"
	case "info":
		return "info"
	case "warn", "warning":
		return "warning"
	case "panic", "fatal", "none":
		return "none"
	default:
		return "error"
	}
}

func xraySOCKSInbound(port int) map[string]any {
	return map[string]any{"protocol": "socks", "tag": "socks-health", "listen": "127.0.0.1", "port": port,
		"settings": map[string]any{"auth": "noauth", "udp": true, "ip": "127.0.0.1"}}
}

func xraySocketOptions() map[string]any {
	options := map[string]any{"domainStrategy": "UseIPv4"}
	if runtime.GOOS == "linux" {
		options["mark"] = WANFwmark
	}
	return options
}

func xrayBaseConfig(srv *VLESSServer) (map[string]any, error) {
	outbounds, err := buildXrayProxyOutbounds(srv)
	if err != nil {
		return nil, err
	}
	outbounds = append(outbounds, map[string]any{
		"tag": "direct", "protocol": "freedom", "settings": map[string]any{
			"targetStrategy": "UseIPv4",
			// Xray 26.9 blocks private destinations by default. This is a LAN
			// client, not a public proxy: retain explicitly configured LAN access.
			"finalRules": []any{map[string]any{"action": "allow", "ip": privateCIDRs}},
		},
		"streamSettings": map[string]any{"sockopt": xraySocketOptions()},
	})
	finalRule := map[string]any{"type": "field", "network": "tcp,udp", "outboundTag": "proxy"}
	routing := map[string]any{"domainStrategy": "AsIs", "rules": []map[string]any{finalRule}}
	config := map[string]any{
		"log":       map[string]any{"loglevel": "none", "access": "none"},
		"outbounds": outbounds, "routing": routing,
		"stats": map[string]any{},
		"policy": map[string]any{
			"levels": map[string]any{"0": map[string]any{"handshake": 4}},
			"system": map[string]any{"statsOutboundUplink": true, "statsOutboundDownlink": true},
		},
	}
	if len(srv.Members) > 0 {
		delete(finalRule, "outboundTag")
		finalRule["balancerTag"] = "proxy"
		routing["balancers"] = []any{map[string]any{
			"tag": "proxy", "selector": []string{"proxy-"}, "fallbackTag": "proxy-1",
			"strategy": map[string]any{"type": "leastPing"},
		}}
		config["observatory"] = map[string]any{
			"subjectSelector": []string{"proxy-"}, "probeURL": defaultPingTestURL,
			"probeInterval": "10m", "enableConcurrency": true,
		}
	}
	return config, nil
}

func buildXrayProxyOutbounds(srv *VLESSServer) ([]map[string]any, error) {
	if len(srv.Members) == 0 {
		out, err := buildXrayVLESSOutbound(srv)
		if err != nil {
			return nil, err
		}
		return []map[string]any{out}, nil
	}
	outbounds := make([]map[string]any, 0, len(srv.Members))
	for i := range srv.Members {
		out, err := buildXrayVLESSOutbound(&srv.Members[i])
		if err != nil {
			return nil, fmt.Errorf("profile member %d: %w", i+1, err)
		}
		out["tag"] = fmt.Sprintf("proxy-%d", i+1)
		outbounds = append(outbounds, out)
	}
	return outbounds, nil
}

func buildXrayVLESSOutbound(srv *VLESSServer) (map[string]any, error) {
	network := normalizeVLESSNetwork(srv.Network)
	if !supportedNetworks[network] {
		return nil, fmt.Errorf("transport %s is not supported by Xray %s", network, BundledXray)
	}
	if srv.PacketEncoding != "" && srv.PacketEncoding != "xudp" {
		return nil, fmt.Errorf("packet encoding %q is not supported by Xray", srv.PacketEncoding)
	}
	flow := srv.Flow
	// Xray's Vision default rejects UDP/443; preserve the manager's all-UDP policy.
	if flow == "xtls-rprx-vision" {
		flow += "-udp443"
	}
	stream := map[string]any{"network": network, "security": orDefault(srv.Security, "none"), "sockopt": xraySocketOptions()}
	switch srv.Security {
	case "reality":
		stream["realitySettings"] = map[string]any{"serverName": orDefault(srv.SNI, srv.Address),
			"fingerprint": orDefault(srv.Fingerprint, "chrome"), "password": srv.PublicKey, "shortId": srv.ShortID, "spiderX": srv.SpiderX}
	case "tls":
		tls := map[string]any{"serverName": orDefault(srv.SNI, srv.Address)}
		if srv.Fingerprint != "" {
			tls["fingerprint"] = srv.Fingerprint
		}
		if alpn := splitCSV(srv.ALPN); len(alpn) > 0 {
			tls["alpn"] = alpn
		}
		stream["tlsSettings"] = tls
	}
	switch network {
	case "tcp":
		stream["network"] = "raw"
	case "ws":
		stream["wsSettings"] = map[string]any{"path": srv.Path, "host": srv.Host}
	case "grpc":
		serviceName := srv.Path
		settings := map[string]any{"authority": srv.Host}
		if strings.HasPrefix(serviceName, "/") && !strings.Contains(strings.TrimPrefix(serviceName, "/"), "/") {
			serviceName = strings.TrimPrefix(serviceName, "/")
		} else if strings.HasPrefix(serviceName, "/") && !strings.Contains(serviceName[strings.LastIndex(serviceName, "/")+1:], "|") {
			// Xray maps both Tun and TunMulti to the same custom method name.
			settings["multiMode"] = true
		}
		settings["serviceName"] = serviceName
		stream["grpcSettings"] = settings
	case "httpupgrade":
		stream["httpupgradeSettings"] = map[string]any{"path": srv.Path, "host": srv.Host}
	case "xhttp":
		xhttp, err := buildXrayXHTTPTransport(srv)
		if err != nil {
			return nil, err
		}
		stream["xhttpSettings"] = xhttp
	}
	return map[string]any{
		"protocol": "vless", "tag": "proxy",
		"settings": map[string]any{"vnext": []any{map[string]any{"address": srv.Address, "port": srv.Port,
			"users": []any{map[string]any{"id": srv.UUID, "encryption": "none", "flow": flow}}}}},
		"streamSettings": stream,
		"mux":            map[string]any{"enabled": false, "concurrency": -1, "xudpConcurrency": 16, "xudpProxyUDP443": "allow"},
	}, nil
}

func buildXrayXHTTPTransport(srv *VLESSServer) (map[string]any, error) {
	transport := map[string]any{}
	if len(srv.Extra) > 0 {
		if err := json.Unmarshal(srv.Extra, &transport); err != nil || transport == nil {
			return nil, fmt.Errorf("invalid XHTTP extra JSON")
		}
		transport = xrayLegacyKeys(transport).(map[string]any)
	}
	delete(transport, "extra")
	for key, value := range map[string]string{
		"mode": srv.Mode, "host": srv.Host, "path": srv.Path, "xPaddingBytes": srv.XPadding,
		"sessionPlacement": srv.SessionPlacement, "sessionKey": srv.SessionKey,
		"seqPlacement": srv.SeqPlacement, "seqKey": srv.SeqKey,
		"uplinkHTTPMethod": srv.UplinkHTTPMethod, "uplinkDataPlacement": srv.UplinkDataPlacement, "uplinkDataKey": srv.UplinkDataKey,
		"xPaddingKey": srv.XPaddingKey, "xPaddingHeader": srv.XPaddingHeader, "xPaddingPlacement": srv.XPaddingPlacement, "xPaddingMethod": srv.XPaddingMethod,
	} {
		if value != "" {
			transport[key] = value
		}
	}
	for key, value := range map[string]bool{"noSSEHeader": srv.NoSSEHeader, "noGRPCHeader": srv.NoGRPCHeader, "xPaddingObfsMode": srv.XPaddingObfsMode} {
		if value {
			transport[key] = true
		}
	}
	if srv.ScMaxBufferedPosts != 0 {
		transport["scMaxBufferedPosts"] = srv.ScMaxBufferedPosts
	}
	if len(srv.XHTTPHeaders) > 0 {
		transport["headers"] = srv.XHTTPHeaders
	}
	for key, raw := range map[string]json.RawMessage{"xmux": srv.Xmux, "downloadSettings": srv.DownloadSettings} {
		if len(raw) == 0 {
			continue
		}
		var value any
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil, fmt.Errorf("invalid XHTTP %s: %w", key, err)
		}
		transport[key] = xrayLegacyKeys(value)
	}
	// Host has its own field in Xray; preserve provider headers without a duplicate.
	if raw, ok := transport["headers"]; ok {
		data, _ := json.Marshal(raw)
		var headers map[string]string
		if err := json.Unmarshal(data, &headers); err != nil {
			return nil, fmt.Errorf("invalid XHTTP headers: %w", err)
		}
		for key, value := range headers {
			if strings.EqualFold(key, "host") {
				if transport["host"] == nil || transport["host"] == "" {
					transport["host"] = value
				}
				delete(headers, key)
			}
		}
		transport["headers"] = headers
	}
	if value, exists := transport["xPaddingBytes"]; !exists || value == "" || value == "0" || value == float64(0) {
		transport["xPaddingBytes"] = "100-1000"
	}
	// A split download opens another socket: it needs the same loop-prevention mark.
	if download, ok := transport["downloadSettings"].(map[string]any); ok {
		sockopt, _ := download["sockopt"].(map[string]any)
		if sockopt == nil {
			sockopt = map[string]any{}
		}
		for key, value := range xraySocketOptions() {
			sockopt[key] = value
		}
		download["sockopt"] = sockopt
	}
	return transport, nil
}

// The saved v1.16.7 schema used snake_case for xmux/downloadSettings.
// Keep those subscriptions usable without changing their IDs or saved URLs.
func xrayLegacyKeys(value any) any {
	obj, ok := value.(map[string]any)
	if !ok {
		return value
	}
	aliases := map[string]string{
		"x_padding_bytes": "xPaddingBytes", "no_grpc_header": "noGRPCHeader", "no_sse_header": "noSSEHeader",
		"sessionIDKey": "sessionKey", "sessionIdKey": "sessionKey", "sessionIDPlacement": "sessionPlacement", "sessionIdPlacement": "sessionPlacement",
		"session_key": "sessionKey", "session_placement": "sessionPlacement", "seq_key": "seqKey", "seq_placement": "seqPlacement",
		"uplink_http_method": "uplinkHTTPMethod", "uplink_httpmethod": "uplinkHTTPMethod", "uplink_data_key": "uplinkDataKey", "uplink_data_placement": "uplinkDataPlacement",
		"max_concurrency": "maxConcurrency", "max_connections": "maxConnections", "cmax_reuse_times": "cMaxReuseTimes", "c_max_reuse_times": "cMaxReuseTimes",
		"hmax_request_times": "hMaxRequestTimes", "h_max_request_times": "hMaxRequestTimes", "hmax_reusable_secs": "hMaxReusableSecs", "h_max_reusable_secs": "hMaxReusableSecs",
		"hkeep_alive_period": "hKeepAlivePeriod", "h_keep_alive_period": "hKeepAlivePeriod",
		"download_settings": "downloadSettings", "download": "downloadSettings", "tls_settings": "tlsSettings", "reality_settings": "realitySettings",
		"xhttp_settings": "xhttpSettings", "splithttp_settings": "splithttpSettings", "server_name": "serverName", "public_key": "publicKey", "short_id": "shortId",
		"x_padding_obfs_mode": "xPaddingObfsMode", "x_padding_key": "xPaddingKey", "x_padding_header": "xPaddingHeader", "x_padding_placement": "xPaddingPlacement", "x_padding_method": "xPaddingMethod",
		"sc_max_each_post_bytes": "scMaxEachPostBytes", "sc_min_posts_interval_ms": "scMinPostsIntervalMs", "sc_max_buffered_posts": "scMaxBufferedPosts", "sc_stream_up_server_secs": "scStreamUpServerSecs",
	}
	out := make(map[string]any, len(obj))
	for key, nested := range obj {
		if alias := aliases[key]; alias != "" {
			if _, canonical := obj[alias]; canonical {
				continue
			}
			key = alias
		}
		// Header names and values must never be rewritten.
		if key == "headers" {
			out[key] = nested
		} else {
			out[key] = xrayLegacyKeys(nested)
		}
	}
	return out
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func camelToSnake(s string) string {
	var b strings.Builder
	for i, r := range s {
		if i > 0 && r >= 'A' && r <= 'Z' && (s[i-1] < 'A' || s[i-1] > 'Z') {
			b.WriteByte('_')
		}
		if r >= 'A' && r <= 'Z' {
			r += 'a' - 'A'
		}
		b.WriteRune(r)
	}
	return b.String()
}

func splitCSV(value string) []string {
	var out []string
	for _, part := range strings.Split(value, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}
