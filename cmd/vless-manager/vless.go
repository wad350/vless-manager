package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// serverFingerprint returns a stable ID for one complete connection profile.
// Providers commonly publish several profiles with the same address, port and
// UUID but different Reality SNI/short IDs or transport settings. Those are
// distinct servers and must be pinged, disabled and selected independently.
func serverFingerprint(srv VLESSServer) string {
	srv.ID = ""
	srv.Name = ""
	srv.Network = normalizeVLESSNetwork(srv.Network)
	data, _ := json.Marshal(srv)
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:8]) // 16 hex chars
}

// normalizeVLESSNetwork converts share-link and Xray naming variants to the
// transport names understood by sing-box. Xray renamed its plain TCP transport
// from "tcp" to "raw"; on the wire they are the same transport (no V2Ray
// transport block), so rejecting raw nodes would discard valid servers.
func normalizeVLESSNetwork(network string) string {
	switch strings.ToLower(strings.TrimSpace(network)) {
	case "", "tcp", "raw":
		return "tcp"
	case "websocket":
		return "ws"
	case "http-upgrade", "http_upgrade":
		return "httpupgrade"
	case "splithttp":
		// Xray's previous name for XHTTP. Official sing-box does not implement
		// either spelling, but canonicalizing it keeps filtering deterministic.
		return "xhttp"
	default:
		return strings.ToLower(strings.TrimSpace(network))
	}
}

// parseVLESSURI parses a vless:// URI into a VLESSServer.
// Format: vless://uuid@host:port?security=reality&sni=...&pbk=...&sid=...&fp=chrome&flow=xtls-rprx-vision&type=tcp#name
func parseVLESSURI(uri string) (*VLESSServer, error) {
	if !strings.HasPrefix(uri, "vless://") {
		return nil, fmt.Errorf("not a vless:// URI")
	}

	u, err := url.Parse(uri)
	if err != nil {
		return nil, fmt.Errorf("parse URI: %w", err)
	}

	uuid := u.User.Username()
	if uuid == "" {
		return nil, fmt.Errorf("missing UUID")
	}

	host := u.Hostname()
	portStr := u.Port()
	if host == "" {
		return nil, fmt.Errorf("missing host")
	}
	port := 443
	if portStr != "" {
		p, err := strconv.Atoi(portStr)
		if err != nil {
			return nil, fmt.Errorf("invalid port: %s", portStr)
		}
		port = p
	}

	q := u.Query()

	name := u.Fragment
	if name == "" {
		name = host
	}
	name, _ = url.QueryUnescape(name)

	network := normalizeVLESSNetwork(q.Get("type"))
	path := q.Get("path")
	if network == "grpc" {
		path = firstNonEmpty(q.Get("serviceName"), q.Get("service_name"), path)
	}

	security := q.Get("security")
	if security == "" {
		security = "none"
	}

	fp := q.Get("fp")
	if fp == "" {
		fp = "chrome"
	}

	srv := &VLESSServer{
		Name:        name,
		Address:     host,
		Port:        port,
		UUID:        uuid,
		Flow:        q.Get("flow"),
		Security:    security,
		SNI:         q.Get("sni"),
		Fingerprint: fp,
		PublicKey:   q.Get("pbk"),
		ShortID:     q.Get("sid"),
		SpiderX:     q.Get("spx"),
		Network:     network,
		Path:        path,
		Host:        q.Get("host"),
		Mode:        q.Get("mode"),
		XPadding:    firstNonEmpty(q.Get("x_padding_bytes"), q.Get("xPaddingBytes")),
		ALPN:        q.Get("alpn"),
	}

	// Most modern xhttp share-links (3x-ui, Marzban, hUI, etc.) carry the
	// transport tuning knobs inside a URL-encoded JSON blob:
	//   ?type=xhttp&extra=%7B%22mode%22%3A%22stream-up%22%2C%22headers%22...%7D
	// Pull every recognized key from there and let explicit ?mode=, ?path=,
	// ?host= override.
	if raw := q.Get("extra"); raw != "" {
		applyXHTTPExtra(srv, raw)
	}

	if srv.SpiderX == "" {
		srv.SpiderX = "/"
	}
	srv.ID = serverFingerprint(*srv)

	return srv, nil
}

// applyXHTTPExtra decodes the `extra=...` URL parameter (URL-encoded JSON used
// by Xray xhttp profiles) and populates fields on srv that weren't already
// supplied via dedicated query params. Anything we don't recognize is left
// untouched.
func applyXHTTPExtra(srv *VLESSServer, raw string) {
	var extra map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &extra); err != nil {
		return
	}
	pickString := func(keys ...string) string {
		for _, k := range keys {
			if v, ok := extra[k]; ok {
				var s string
				if err := json.Unmarshal(v, &s); err == nil && s != "" {
					return s
				}
			}
		}
		return ""
	}
	pickBool := func(keys ...string) (bool, bool) {
		for _, k := range keys {
			if v, ok := extra[k]; ok {
				var b bool
				if err := json.Unmarshal(v, &b); err == nil {
					return b, true
				}
			}
		}
		return false, false
	}
	pickInt := func(keys ...string) (int64, bool) {
		for _, k := range keys {
			if v, ok := extra[k]; ok {
				var n int64
				if err := json.Unmarshal(v, &n); err == nil {
					return n, true
				}
			}
		}
		return 0, false
	}

	if srv.Mode == "" {
		srv.Mode = pickString("mode")
	}
	if srv.Path == "" {
		srv.Path = pickString("path")
	}
	if srv.Host == "" {
		srv.Host = pickString("host")
	}
	if srv.XPadding == "" {
		srv.XPadding = pickString("xPaddingBytes", "x_padding_bytes")
	}
	if v := pickString("xPaddingKey", "x_padding_key"); v != "" && srv.XPaddingKey == "" {
		srv.XPaddingKey = v
	}
	if v := pickString("xPaddingHeader", "x_padding_header"); v != "" && srv.XPaddingHeader == "" {
		srv.XPaddingHeader = v
	}
	if v := pickString("xPaddingPlacement", "x_padding_placement"); v != "" && srv.XPaddingPlacement == "" {
		srv.XPaddingPlacement = v
	}
	if v := pickString("xPaddingMethod", "x_padding_method"); v != "" && srv.XPaddingMethod == "" {
		srv.XPaddingMethod = v
	}
	if b, ok := pickBool("xPaddingObfsMode", "x_padding_obfs_mode"); ok {
		srv.XPaddingObfsMode = b
	}
	if b, ok := pickBool("noGRPCHeader", "no_grpc_header"); ok {
		srv.NoGRPCHeader = b
	}
	if b, ok := pickBool("noSSEHeader", "no_sse_header"); ok {
		srv.NoSSEHeader = b
	}
	if v := pickString("sessionPlacement", "session_placement"); v != "" {
		srv.SessionPlacement = v
	}
	if v := pickString("sessionKey", "session_key"); v != "" {
		srv.SessionKey = v
	}
	if v := pickString("seqPlacement", "seq_placement"); v != "" {
		srv.SeqPlacement = v
	}
	if v := pickString("seqKey", "seq_key"); v != "" {
		srv.SeqKey = v
	}
	if v := pickString("uplinkHTTPMethod", "uplink_http_method"); v != "" {
		srv.UplinkHTTPMethod = v
	}
	if v := pickString("uplinkDataPlacement", "uplink_data_placement"); v != "" {
		srv.UplinkDataPlacement = v
	}
	if v := pickString("uplinkDataKey", "uplink_data_key"); v != "" {
		srv.UplinkDataKey = v
	}
	if n, ok := pickInt("scMaxBufferedPosts", "sc_max_buffered_posts"); ok {
		srv.ScMaxBufferedPosts = n
	}

	if v, ok := extra["headers"]; ok {
		var hdrs map[string]string
		if err := json.Unmarshal(v, &hdrs); err == nil && len(hdrs) > 0 {
			srv.XHTTPHeaders = make(map[string]string, len(hdrs))
			for k, val := range hdrs {
				// Keep Host in the dedicated XHTTP field. The extended engine
				// rejects Host inside the generic headers object.
				if strings.EqualFold(k, "host") {
					if srv.Host == "" {
						srv.Host = val
					}
					continue
				}
				srv.XHTTPHeaders[k] = val
			}
			if len(srv.XHTTPHeaders) == 0 {
				srv.XHTTPHeaders = nil
			}
		}
	}

	if v, ok := extra["xmux"]; ok && len(v) > 0 && string(v) != "null" {
		srv.Xmux = normalizeRawObjectKeys(v)
	}
	if v, ok := extra["downloadSettings"]; ok && len(v) > 0 && string(v) != "null" {
		srv.DownloadSettings = normalizeRawObjectKeys(v)
	}

	// Stash the full extra blob so singbox.go can replay every recognised
	// camelCase key into the xhttp transport — keeps us forward-compatible
	// with new Xray fields without re-touching the parser.
	srv.Extra = json.RawMessage([]byte(raw))
}

func normalizeRawObjectKeys(raw json.RawMessage) json.RawMessage {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil || len(obj) == 0 {
		return append(json.RawMessage(nil), raw...)
	}
	normalized := make(map[string]json.RawMessage, len(obj))
	for key, value := range obj {
		normalized[camelToSnake(key)] = value
	}
	data, err := json.Marshal(normalized)
	if err != nil {
		return append(json.RawMessage(nil), raw...)
	}
	return data
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
