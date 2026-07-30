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
	if isSupportedServer(srv) {
		t.Fatal("xhttp must be excluded by official sing-box")
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

func TestBuildSingBoxRejectsXHTTP(t *testing.T) {
	_, err := buildSingBoxVLESSOutbound(&VLESSServer{
		Address: "example.com",
		Port:    443,
		UUID:    "00000000-0000-0000-0000-000000000000",
		Network: "xhttp",
		Mode:    "stream-up",
		Host:    "cdn.example",
		Path:    "/app",
	})
	if err == nil {
		t.Fatal("official sing-box must reject xhttp")
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
