//go:build with_utls

package main

import (
	"encoding/json"
	"testing"
)

func TestExtendedEngineAcceptsGeneratedXHTTPOutbound(t *testing.T) {
	srv := &VLESSServer{
		Address: "example.com", Port: 443, UUID: "00000000-0000-0000-0000-000000000000",
		Network: "xhttp", Security: "tls", SNI: "example.com", Fingerprint: "chrome",
		Host: "cdn.example", Path: "/api/sync", Mode: "auto",
		Extra: json.RawMessage(`{
			"xPaddingBytes":"","scMaxEachPostBytes":0,"noSSEHeader":false,"sessionIDKey":"X-Auth-Token","sessionIDPlacement":"header",
			"seqKey":"page","seqPlacement":"query","uplinkHTTPMethod":"POST",
			"xmux":{"maxConcurrency":"16-32","maxConnections":"0","hKeepAlivePeriod":0}
		}`),
	}
	outbound, err := buildSingBoxVLESSOutbound(srv)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := json.Marshal(map[string]any{
		"log": map[string]any{"level": "error"},
		"inbounds": []any{map[string]any{
			"type": "socks", "tag": "socks-in", "listen": "127.0.0.1", "listen_port": 0,
		}},
		"outbounds": []any{outbound},
		"route":     map[string]any{"final": "proxy"},
	})
	if err != nil {
		t.Fatal(err)
	}
	instance, err := newBoxInstance(t.Context(), cfg, newRingBuffer())
	if err != nil {
		t.Fatalf("extended sing-box rejected generated XHTTP config: %v\n%s", err, cfg)
	}
	if err := instance.Close(); err != nil {
		t.Fatal(err)
	}
}
