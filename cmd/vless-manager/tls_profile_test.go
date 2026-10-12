package main

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"
)

func TestTLSProfileIsOptInAndDoesNotModifyCatalog(t *testing.T) {
	server := VLESSServer{ID: "unchanged", Fingerprint: "chrome", Security: "reality", Members: []VLESSServer{
		{Fingerprint: "firefox", Security: "tls"}, {Fingerprint: "safari", Security: "none"},
	}}
	before := cloneVLESSServer(server)
	if got := serverWithTLSProfile(server, "server"); !reflect.DeepEqual(got, before) {
		t.Fatal("default changed the subscription profile")
	}
	got := serverWithTLSProfile(server, "randomized-chacha20")
	if got.ID != before.ID || got.Fingerprint != "randomized-chacha20" ||
		got.Members[0].Fingerprint != "randomized-chacha20" || got.Members[1].Fingerprint != "safari" {
		t.Fatalf("wrong runtime profile: %+v", got)
	}
	if !reflect.DeepEqual(server, before) {
		t.Fatal("subscription was modified")
	}
	cfg := defaultConfig()
	cfg.Settings.TLSClientProfile = "randomized-chacha20"
	server.Members = nil
	server.Network = "grpc"
	server.PublicKey = "test"
	data, err := generateXrayConfig(cfg, &server)
	if err != nil {
		t.Fatal(err)
	}
	var generated map[string]any
	if err := json.Unmarshal(data, &generated); err != nil {
		t.Fatal(err)
	}
	stream := generated["outbounds"].([]any)[0].(map[string]any)["streamSettings"].(map[string]any)
	if stream["realitySettings"].(map[string]any)["fingerprint"] != "randomized-chacha20" {
		t.Fatal("runtime did not use the chosen profile")
	}
}

func TestTLSProfileSettingsValidationAndPingIsolation(t *testing.T) {
	api := newHandlerTestAPI(t)
	api.lastPing = []PingResult{{ServerID: "old", LatencyMS: 10}}
	api.pingRunMu.Lock()
	w := apiRequest(t, api, http.MethodPatch, "/api/settings", `{"tls_client_profile":"randomized-chacha20"}`)
	api.pingRunMu.Unlock()
	if w.Code != http.StatusConflict || api.cfg.Settings.TLSClientProfile != "server" {
		t.Fatalf("profile changed during ping: %d", w.Code)
	}
	w = apiRequest(t, api, http.MethodPatch, "/api/settings", `{"tls_client_profile":"invalid"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid profile accepted: %d", w.Code)
	}
	w = apiRequest(t, api, http.MethodPatch, "/api/settings", `{"tls_client_profile":"randomized-chacha20"}`)
	if w.Code != http.StatusOK || len(api.lastPing) != 0 || api.cfg.Settings.TLSClientProfile != "randomized-chacha20" {
		t.Fatalf("profile not applied or old ping reused: %d %s", w.Code, w.Body.String())
	}
}
