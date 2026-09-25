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
		t.Fatal("xhttp must be supported by Xray")
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
	out, err := buildXrayVLESSOutbound(srv)
	if err != nil {
		t.Fatal(err)
	}
	if out["streamSettings"].(map[string]any)["network"] != "raw" {
		t.Fatalf("TCP must use raw: %#v", out)
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
	out, err := buildXrayVLESSOutbound(xudp)
	if err != nil {
		t.Fatal(err)
	}
	if out["mux"].(map[string]any)["xudpConcurrency"] != 16 {
		t.Fatalf("XUDP not enabled: %#v", out)
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
	out, err := buildXrayVLESSOutbound(srv)
	if err != nil {
		t.Fatal(err)
	}
	stream := out["streamSettings"].(map[string]any)
	transport, ok := stream["grpcSettings"].(map[string]any)
	if !ok || stream["network"] != "grpc" || transport["serviceName"] != "artemida-grpc" {
		t.Fatalf("gRPC transport generated incorrectly: %#v", stream)
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

func TestBuildXraySupportsXHTTP(t *testing.T) {
	out, err := buildXrayVLESSOutbound(&VLESSServer{
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
	transport := out["streamSettings"].(map[string]any)["xhttpSettings"].(map[string]any)
	if transport["mode"] != "stream-up" || transport["host"] != "cdn.example" || transport["path"] != "/app" {
		t.Fatalf("XHTTP transport generated incorrectly: %#v", transport)
	}
	if transport["xPaddingBytes"] != "100-1000" {
		t.Fatalf("default XHTTP padding = %v", transport["xPaddingBytes"])
	}
}

func TestBuildXrayXHTTPPreservesExtendedOptions(t *testing.T) {
	extra := json.RawMessage(`{"mode":"auto","xPaddingBytes":"","scMaxEachPostBytes":0,"noSSEHeader":false,"uplinkHTTPMethod":"POST","sessionIDKey":"X-Auth-Token","sessionIDPlacement":"header","xmux":{"maxConcurrency":"16-32","hKeepAlivePeriod":0}}`)
	out, err := buildXrayVLESSOutbound(&VLESSServer{
		Address: "example.com", Port: 443, UUID: "00000000-0000-0000-0000-000000000000",
		Network: "xhttp", Security: "tls", SNI: "example.com", Path: "/sync", Host: "cdn.example",
		Extra: extra,
	})
	if err != nil {
		t.Fatal(err)
	}
	transport := out["streamSettings"].(map[string]any)["xhttpSettings"].(map[string]any)
	if transport["sessionKey"] != "X-Auth-Token" || transport["sessionPlacement"] != "header" || transport["uplinkHTTPMethod"] != "POST" {
		t.Fatalf("extended fields lost: %#v", transport)
	}
	if transport["xPaddingBytes"] != "100-1000" {
		t.Fatalf("empty provider padding was not normalized: %#v", transport)
	}
	if transport["scMaxEachPostBytes"] != float64(0) {
		t.Fatalf("provider post size not retained: %#v", transport)
	}
	xmux := transport["xmux"].(map[string]any)
	if xmux["maxConcurrency"] != "16-32" || xmux["hKeepAlivePeriod"] != float64(0) {
		t.Fatalf("xmux fields lost: %#v", xmux)
	}
}

func TestBuildXrayRejectsRemovedQUICTransport(t *testing.T) {
	_, err := buildXrayVLESSOutbound(&VLESSServer{
		Address:  "example.com",
		Port:     443,
		UUID:     "00000000-0000-0000-0000-000000000000",
		Network:  "quic",
		Security: "tls",
		SNI:      "example.com",
	})
	if err == nil {
		t.Fatal("removed QUIC transport must be rejected")
	}
}

func TestBuildXrayDefaultsUDPToXUDP(t *testing.T) {
	out, err := buildXrayVLESSOutbound(&VLESSServer{
		Address: "example.com", Port: 443,
		UUID: "00000000-0000-0000-0000-000000000000", Network: "tcp",
	})
	if err != nil {
		t.Fatal(err)
	}
	mux := out["mux"].(map[string]any)
	if mux["xudpConcurrency"] != 16 || mux["xudpProxyUDP443"] != "allow" {
		t.Fatalf("unexpected XUDP settings: %#v", mux)
	}
}

func TestGeneratedTunMTUIsFixedAt1500(t *testing.T) {
	cfg := defaultConfig()
	cfg.Settings.BypassRouteRussia = false
	data, err := generateXrayConfig(cfg, &VLESSServer{
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
	tun := inbounds[0].(map[string]any)["settings"].(map[string]any)
	if tun["mtu"] != float64(1500) {
		t.Fatalf("TUN MTU = %v, want 1500", tun["mtu"])
	}
	addresses, ok := tun["gateway"].([]any)
	if !ok || len(addresses) != 1 || addresses[0] != tunAddr {
		t.Fatalf("TUN gateway = %v, want [%q]", tun["gateway"], tunAddr)
	}
	if _, exists := tun["inet4_address"]; exists {
		t.Fatal("generated config contains removed inet4_address field")
	}
}

func TestGeneratedConfigRoutesPublicTCPUDPThroughProxy(t *testing.T) {
	cfg := defaultConfig()
	cfg.Settings.BypassRouteRussia = false
	data, err := generateXrayConfig(cfg, &VLESSServer{
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
	route := generated["routing"].(map[string]any)
	rules := route["rules"].([]any)
	if len(rules) < 2 {
		t.Fatalf("route rules = %#v, want private bypass and proxy rules", rules)
	}
	privateRule := rules[0].(map[string]any)
	if privateRule["ip"] == nil || privateRule["outboundTag"] != "direct" {
		t.Fatalf("first route rule = %#v, want private bypass", privateRule)
	}
	if final := rules[len(rules)-1].(map[string]any); final["network"] != "tcp,udp" || final["outboundTag"] != "proxy" {
		t.Fatalf("unexpected final rule: %#v", final)
	}
	if len(tunnelProtocols) != 2 || tunnelProtocols[0] != "tcp" || tunnelProtocols[1] != "udp" {
		t.Fatalf("unsupported IP protocols must stay outside TUN: %v", tunnelProtocols)
	}
}

func TestBuildXrayAutoProfile(t *testing.T) {
	profile := &VLESSServer{Name: "Auto", Members: []VLESSServer{
		{Address: "one.example", Port: 443, UUID: "00000000-0000-0000-0000-000000000301", Network: "tcp"},
		{Address: "two.example", Port: 443, UUID: "00000000-0000-0000-0000-000000000302", Network: "grpc", Path: "grpc"},
	}}
	outs, err := buildXrayProxyOutbounds(profile)
	if err != nil {
		t.Fatal(err)
	}
	if len(outs) != 2 || outs[0]["tag"] != "proxy-1" || outs[1]["tag"] != "proxy-2" {
		t.Fatalf("unexpected profile outbounds: %#v", outs)
	}
	config, err := xrayBaseConfig(profile)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := parseXrayConfig(data); err != nil {
		t.Fatal(err)
	}
	if config["observatory"] == nil || config["routing"].(map[string]any)["balancers"] == nil {
		t.Fatal("profile lacks automatic selection")
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
