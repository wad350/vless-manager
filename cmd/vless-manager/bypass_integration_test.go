package main

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/proxy"
)

func TestGeneratedBypassSniffingAndDomainBoundaries(t *testing.T) {
	const payload = "routing-policy-response"
	for _, encrypted := range []bool{false, true} {
		t.Run(fmt.Sprintf("TLS=%v", encrypted), func(t *testing.T) {
			handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, payload)
			})
			var target *httptest.Server
			if encrypted {
				target = httptest.NewTLSServer(handler)
			} else {
				target = httptest.NewServer(handler)
			}
			defer target.Close()
			sink := target.Listener.Addr().String()
			serverPort := unusedTCPPort(t)
			server := &VLESSServer{Address: "127.0.0.1", Port: serverPort, UUID: integrationUUID, Network: "grpc", Path: "test"}
			startTestXray(t, map[string]any{
				"inbounds": []any{map[string]any{
					"listen": "127.0.0.1", "port": serverPort, "protocol": "vless",
					"settings":       map[string]any{"decryption": "none", "clients": []any{map[string]any{"id": integrationUUID}}},
					"streamSettings": map[string]any{"network": "grpc", "grpcSettings": map[string]any{"serviceName": "test"}},
				}},
				"outbounds": []any{map[string]any{"protocol": "freedom", "settings": map[string]any{
					"redirect":   sink,
					"finalRules": []any{map[string]any{"action": "allow", "ip": []string{"127.0.0.0/8"}}},
				}}},
			}, false)
			for _, tc := range []struct {
				host   string
				direct bool
			}{
				{"bypass.test", true},
				{"nested.bypass.test", true},
				{"notbypass.test", false},
				{"bypass.test.evil.test", false},
				{"203.0.113.20", false},
			} {
				t.Run(tc.host, func(t *testing.T) {
					cfg := defaultConfig()
					cfg.Settings.BypassRouteRussia = false
					cfg.Settings.BypassDomains = []string{"bypass.test"}
					data, err := generateXrayConfig(cfg, server)
					if err != nil {
						t.Fatal(err)
					}
					var config map[string]any
					if err := json.Unmarshal(data, &config); err != nil {
						t.Fatal(err)
					}
					port := unusedTCPPort(t)
					inbound := xraySOCKSInbound(port)
					inbound["tag"] = "routing-test"
					inbound["sniffing"] = config["inbounds"].([]any)[0].(map[string]any)["sniffing"]
					config["inbounds"] = []any{inbound}
					// Keep the generated route rules intact. Redirect both exits to
					// the local fixture so a public destination needs no internet.
					for _, raw := range config["outbounds"].([]any) {
						out := raw.(map[string]any)
						if stream, ok := out["streamSettings"].(map[string]any); ok {
							if options, ok := stream["sockopt"].(map[string]any); ok {
								delete(options, "mark")
							}
						}
						if out["tag"] == "direct" {
							out["settings"].(map[string]any)["redirect"] = sink
						}
					}
					core := startTestXray(t, config, false)
					dialer, err := proxy.SOCKS5("tcp", fmt.Sprintf("127.0.0.1:%d", port), nil, nil)
					if err != nil {
						t.Fatal(err)
					}
					transport := &http.Transport{Dial: dialer.Dial, DisableKeepAlives: true}
					if encrypted {
						// Only the local httptest certificate is used in this fixture.
						transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true, ServerName: tc.host}
					}
					defer transport.CloseIdleConnections()
					url := strings.Replace(target.URL, sink, "203.0.113.20:18080", 1)
					req, err := http.NewRequest(http.MethodGet, url, nil)
					if err != nil {
						t.Fatal(err)
					}
					req.Host = tc.host
					resp, err := (&http.Client{Transport: transport, Timeout: 5 * time.Second}).Do(req)
					if err != nil {
						t.Fatal(err)
					}
					body, err := io.ReadAll(resp.Body)
					resp.Body.Close()
					if err != nil || string(body) != payload {
						t.Fatalf("response = %q, error = %v", body, err)
					}
					deadline := time.Now().Add(time.Second)
					for {
						stats := core.TrafficSnapshot()
						if tc.direct && stats.BypassDownload >= uint64(len(payload)) && stats.BypassUpload > 0 && stats.VPNDownload == 0 && stats.VPNUpload == 0 {
							break
						}
						if !tc.direct && stats.VPNDownload >= uint64(len(payload)) && stats.VPNUpload > 0 && stats.BypassDownload == 0 && stats.BypassUpload == 0 {
							break
						}
						if time.Now().After(deadline) {
							t.Fatalf("direct=%v, traffic=%+v", tc.direct, stats)
						}
						time.Sleep(10 * time.Millisecond)
					}
				})
			}
		})
	}
}
