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
	outbound, err := buildXrayVLESSOutbound(srv)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := json.Marshal(map[string]any{
		"log":       map[string]any{"loglevel": "error"},
		"inbounds":  []any{xraySOCKSInbound(12345)},
		"outbounds": []any{outbound},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = parseXrayConfig(cfg)
	if err != nil {
		t.Fatalf("Xray rejected generated XHTTP config: %v\n%s", err, cfg)
	}
}
