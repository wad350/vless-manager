package main

import (
	"encoding/json"
	"net"
	"testing"
)

func TestServerFingerprintIncludesConnectionProfile(t *testing.T) {
	base := VLESSServer{
		Address:   "example.com",
		Port:      443,
		UUID:      "00000000-0000-0000-0000-000000000001",
		Security:  "reality",
		Network:   "tcp",
		PublicKey: "public-key",
	}
	first := base
	first.SNI = "one.example"
	first.ShortID = "1111"
	second := base
	second.SNI = "two.example"
	second.ShortID = "2222"

	if serverFingerprint(first) == serverFingerprint(second) {
		t.Fatal("profiles with different Reality parameters have the same ID")
	}
	first.Name = "renamed"
	if serverFingerprint(first) != serverFingerprint(VLESSServer{
		Address:   first.Address,
		Port:      first.Port,
		UUID:      first.UUID,
		Security:  first.Security,
		SNI:       first.SNI,
		PublicKey: first.PublicKey,
		ShortID:   first.ShortID,
		Network:   first.Network,
	}) {
		t.Fatal("display name must not affect server ID")
	}
}

func TestParseVLESSXHTTPURI(t *testing.T) {
	srv, err := parseVLESSURI("vless://00000000-0000-0000-0000-000000000000@example.com:443?type=xhttp&security=tls&sni=front.example&fp=chrome&path=%2Fapp&host=cdn.example&mode=packet-up&x_padding_bytes=100-1000#xhttp")
	if err != nil {
		t.Fatal(err)
	}
	if srv.Network != "xhttp" {
		t.Fatalf("network = %q, want xhttp", srv.Network)
	}
	if !isSupportedServer(srv) {
		t.Fatal("xhttp must be supported by extended sing-box")
	}
}

func TestParseVLESSNormalizesRawToTCP(t *testing.T) {
	srv, err := parseVLESSURI("vless://00000000-0000-0000-0000-000000000000@example.com:443?type=raw&security=reality#raw")
	if err != nil {
		t.Fatal(err)
	}
	if srv.Network != "tcp" || !isSupportedServer(srv) {
		t.Fatalf("raw transport parsed as network=%q supported=%v", srv.Network, isSupportedServer(srv))
	}
	out, err := buildSingBoxVLESSOutbound(srv)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := out["transport"]; exists {
		t.Fatalf("plain TCP/raw outbound must not contain a transport block: %#v", out["transport"])
	}
}

func TestParseVLESSPacketEncoding(t *testing.T) {
	const base = "vless://00000000-0000-0000-0000-000000000000@example.com:443?type=tcp&security=reality"
	plain, err := parseVLESSURI(base + "#plain")
	if err != nil {
		t.Fatal(err)
	}
	xudp, err := parseVLESSURI(base + "&packet-encoding=xudp#xudp")
	if err != nil {
		t.Fatal(err)
	}
	if xudp.PacketEncoding != "xudp" {
		t.Fatalf("packet encoding = %q, want xudp", xudp.PacketEncoding)
	}
	if serverFingerprint(*plain) == serverFingerprint(*xudp) {
		t.Fatal("profiles with different packet encodings have the same ID")
	}
	out, err := buildSingBoxVLESSOutbound(xudp)
	if err != nil {
		t.Fatal(err)
	}
	if out["packet_encoding"] != "xudp" {
		t.Fatalf("packet_encoding = %v, want xudp", out["packet_encoding"])
	}
}

func TestParseVLESSGRPCServiceName(t *testing.T) {
	srv, err := parseVLESSURI("vless://00000000-0000-0000-0000-000000000000@example.com:443?type=grpc&security=reality&serviceName=artemida-grpc&path=wrong#grpc")
	if err != nil {
		t.Fatal(err)
	}
	if srv.Network != "grpc" || srv.Path != "artemida-grpc" || !isSupportedServer(srv) {
		t.Fatalf("gRPC transport parsed incorrectly: %+v", srv)
	}
	out, err := buildSingBoxVLESSOutbound(srv)
	if err != nil {
		t.Fatal(err)
	}
	transport, ok := out["transport"].(map[string]any)
	if !ok || transport["type"] != "grpc" || transport["service_name"] != "artemida-grpc" {
		t.Fatalf("gRPC transport generated incorrectly: %#v", out["transport"])
	}
}

func TestNormalizeVLESSNetworkAliases(t *testing.T) {
	tests := map[string]string{
		"":             "tcp",
		"RAW":          "tcp",
		"websocket":    "ws",
		"http-upgrade": "httpupgrade",
		"http_upgrade": "httpupgrade",
		"splithttp":    "xhttp",
		"kcp":          "kcp",
	}
	for input, want := range tests {
		if got := normalizeVLESSNetwork(input); got != want {
			t.Errorf("normalizeVLESSNetwork(%q)=%q, want %q", input, got, want)
		}
	}
}

func TestParseVLESSXHTTPExtra(t *testing.T) {
	const extra = `{"mode":"stream-up","headers":{"Auth-Token":"secret","Host":"hidden.example"},` +
		`"xPaddingBytes":"200-1500","noGRPCHeader":true,"scMaxBufferedPosts":42}`
	uri := "vless://00000000-0000-0000-0000-000000000000@example.com:443?type=xhttp&security=tls&extra=" + escape(extra) + "#xhttp"
	srv, err := parseVLESSURI(uri)
	if err != nil {
		t.Fatal(err)
	}
	if srv.Mode != "stream-up" || srv.Host != "hidden.example" {
		t.Fatalf("xhttp metadata was not parsed: %#v", srv)
	}
}

func TestBuildSingBoxSupportsXHTTP(t *testing.T) {
	out, err := buildSingBoxVLESSOutbound(&VLESSServer{
		Address: "example.com",
		Port:    443,
		UUID:    "00000000-0000-0000-0000-000000000000",
		Network: "xhttp",
		Mode:    "stream-up",
		Host:    "cdn.example",
		Path:    "/app",
	})
	if err != nil {
		t.Fatal(err)
	}
	transport := out["transport"].(map[string]any)
	if transport["type"] != "xhttp" || transport["mode"] != "stream-up" || transport["host"] != "cdn.example" || transport["path"] != "/app" {
		t.Fatalf("XHTTP transport generated incorrectly: %#v", transport)
	}
	if transport["x_padding_bytes"] != "100-1000" {
		t.Fatalf("default XHTTP padding = %v", transport["x_padding_bytes"])
	}
}

func TestBuildSingBoxXHTTPPreservesExtendedOptions(t *testing.T) {
	extra := json.RawMessage(`{"mode":"auto","xPaddingBytes":"","scMaxEachPostBytes":0,"noSSEHeader":false,"uplinkHTTPMethod":"POST","sessionIDKey":"X-Auth-Token","sessionIDPlacement":"header","xmux":{"maxConcurrency":"16-32","hKeepAlivePeriod":0}}`)
	out, err := buildSingBoxVLESSOutbound(&VLESSServer{
		Address: "example.com", Port: 443, UUID: "00000000-0000-0000-0000-000000000000",
		Network: "xhttp", Security: "tls", SNI: "example.com", Path: "/sync", Host: "cdn.example",
		Extra: extra,
	})
	if err != nil {
		t.Fatal(err)
	}
	transport := out["transport"].(map[string]any)
	if transport["session_key"] != "X-Auth-Token" || transport["session_placement"] != "header" || transport["uplink_http_method"] != "POST" {
		t.Fatalf("extended fields lost: %#v", transport)
	}
	if transport["x_padding_bytes"] != "100-1000" {
		t.Fatalf("empty provider padding was not normalized: %#v", transport)
	}
	if _, exists := transport["sc_max_each_post_bytes"]; exists {
		t.Fatalf("zero provider post size must use the engine default: %#v", transport)
	}
	xmux := transport["xmux"].(map[string]any)
	if xmux["max_concurrency"] != "16-32" || xmux["h_keep_alive_period"] != float64(0) {
		t.Fatalf("xmux fields lost: %#v", xmux)
	}
}

func TestBuildSingBoxSupportsQUIC(t *testing.T) {
	out, err := buildSingBoxVLESSOutbound(&VLESSServer{
		Address:  "example.com",
		Port:     443,
		UUID:     "00000000-0000-0000-0000-000000000000",
		Network:  "quic",
		Security: "tls",
		SNI:      "example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	transport := out["transport"].(map[string]any)
	if transport["type"] != "quic" {
		t.Fatalf("transport.type = %v, want quic", transport["type"])
	}
}

func TestBuildSingBoxDefaultsUDPToXUDP(t *testing.T) {
	out, err := buildSingBoxVLESSOutbound(&VLESSServer{
		Address: "example.com", Port: 443,
		UUID: "00000000-0000-0000-0000-000000000000", Network: "tcp",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out["packet_encoding"] != "xudp" {
		t.Fatalf("default packet_encoding = %v, want xudp", out["packet_encoding"])
	}
}

func TestGeneratedTunMTUIsFixedAt1500(t *testing.T) {
	cfg := defaultConfig()
	cfg.Settings.BypassRouteRussia = false
	data, err := generateSingBoxConfig(cfg, &VLESSServer{
		Address: "example.com",
		Port:    443,
		UUID:    "00000000-0000-0000-0000-000000000001",
		Network: "tcp",
	})
	if err != nil {
		t.Fatal(err)
	}
	var generated map[string]any
	if err := json.Unmarshal(data, &generated); err != nil {
		t.Fatal(err)
	}
	inbounds := generated["inbounds"].([]any)
	tun := inbounds[0].(map[string]any)
	if tun["mtu"] != float64(1500) {
		t.Fatalf("TUN MTU = %v, want 1500", tun["mtu"])
	}
	addresses, ok := tun["address"].([]any)
	if !ok || len(addresses) != 1 || addresses[0] != tunAddr {
		t.Fatalf("TUN address = %v, want [%q]", tun["address"], tunAddr)
	}
	if _, exists := tun["inet4_address"]; exists {
		t.Fatal("generated config contains removed inet4_address field")
	}
}

func TestGeneratedConfigRoutesICMPDirectBeforeSniff(t *testing.T) {
	cfg := defaultConfig()
	cfg.Settings.BypassRouteRussia = false
	data, err := generateSingBoxConfig(cfg, &VLESSServer{
		Address: "example.com",
		Port:    443,
		UUID:    "00000000-0000-0000-0000-000000000001",
		Network: "tcp",
	})
	if err != nil {
		t.Fatal(err)
	}
	var generated map[string]any
	if err := json.Unmarshal(data, &generated); err != nil {
		t.Fatal(err)
	}
	route := generated["route"].(map[string]any)
	rules := route["rules"].([]any)
	if len(rules) < 2 {
		t.Fatalf("route rules = %#v, want ICMP and sniff rules", rules)
	}
	icmpRule := rules[0].(map[string]any)
	if icmpRule["network"] != "icmp" || icmpRule["outbound"] != "direct" {
		t.Fatalf("first route rule = %#v, want ICMP through direct", icmpRule)
	}
	if sniffRule := rules[1].(map[string]any); sniffRule["action"] != "sniff" {
		t.Fatalf("second route rule = %#v, want sniff action", sniffRule)
	}
}

func TestBuildSingBoxAutoProfile(t *testing.T) {
	profile := &VLESSServer{Name: "Auto", Members: []VLESSServer{
		{Address: "one.example", Port: 443, UUID: "00000000-0000-0000-0000-000000000301", Network: "tcp"},
		{Address: "two.example", Port: 443, UUID: "00000000-0000-0000-0000-000000000302", Network: "grpc", Path: "grpc"},
	}}
	outs, err := buildSingBoxProxyOutbounds(profile)
	if err != nil {
		t.Fatal(err)
	}
	if len(outs) != 3 || outs[2]["type"] != "urltest" || outs[2]["tag"] != "proxy" {
		t.Fatalf("unexpected profile outbounds: %#v", outs)
	}
	tags, ok := outs[2]["outbounds"].([]string)
	if !ok || len(tags) != 2 || tags[0] != "proxy-1" || tags[1] != "proxy-2" {
		t.Fatalf("unexpected urltest members: %#v", outs[2]["outbounds"])
	}
}

func TestPinVLESSServerPreservesTLSIdentity(t *testing.T) {
	original := &VLESSServer{
		Address:  "127.0.0.1",
		Port:     443,
		UUID:     "00000000-0000-0000-0000-000000000001",
		Network:  "ws",
		Security: "tls",
		SNI:      "front.example",
		Host:     "cdn.example",
	}
	pinned, dialIP, err := pinVLESSServer(original)
	if err != nil {
		t.Fatal(err)
	}
	if net.ParseIP(dialIP) == nil || pinned.Address != dialIP {
		t.Fatalf("pinned address = %q, dial IP = %q", pinned.Address, dialIP)
	}
	if pinned.SNI != original.SNI || pinned.Host != original.Host {
		t.Fatalf("TLS identity changed: SNI=%q Host=%q", pinned.SNI, pinned.Host)
	}
	if original.Address != "127.0.0.1" {
		t.Fatal("pinVLESSServer mutated the source server")
	}
}

func escape(s string) string {
	out := make([]byte, 0, len(s)*3)
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9', c == '-' || c == '_' || c == '.' || c == '~':
			out = append(out, c)
		default:
			out = append(out, '%')
			out = append(out, hexChar(c>>4))
			out = append(out, hexChar(c&0xF))
		}
	}
	return string(out)
}

func hexChar(b byte) byte {
	if b < 10 {
		return '0' + b
	}
	return 'A' + (b - 10)
}
