package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	xlog "github.com/xtls/xray-core/common/log"
	_ "github.com/xtls/xray-core/proxy/vless/inbound"
	"golang.org/x/net/proxy"
)

const integrationUUID = "00000000-0000-0000-0000-000000000001"

func unusedTCPPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port
}

func startTestXray(t *testing.T, config map[string]any, main bool) engineHandle {
	t.Helper()
	// Loopback integration tests must also run on unprivileged Linux CI.
	if outbounds, ok := config["outbounds"].([]map[string]any); ok {
		for _, out := range outbounds {
			if stream, ok := out["streamSettings"].(map[string]any); ok {
				if sockopt, ok := stream["sockopt"].(map[string]any); ok {
					delete(sockopt, "mark")
				}
			}
		}
	}
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	logs := newRingBuffer()
	b, err := startXray(data, logs, main)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		b.Close()
		if t.Failed() {
			entries, _ := logs.Entries(0)
			for _, entry := range entries {
				t.Log(entry.Message)
			}
		}
	})
	return b
}

// Two independent Xray instances exchange real VLESS traffic over every
// advertised transport. A TCP connect alone would not catch config regressions.
func TestXrayTransportsTCPUDPAndTraffic(t *testing.T) {
	for _, network := range []string{"tcp", "ws", "grpc", "grpc-custom", "httpupgrade", "xhttp"} {
		t.Run(network, func(t *testing.T) {
			serverPort := unusedTCPPort(t)
			networkType := network
			path := "/test"
			if network == "grpc-custom" {
				networkType, path = "grpc", "/service/Method"
			}
			srv := &VLESSServer{Address: "127.0.0.1", Port: serverPort, UUID: integrationUUID, Network: networkType, Path: path, Mode: "packet-up"}
			out, err := buildXrayVLESSOutbound(srv)
			if err != nil {
				t.Fatal(err)
			}
			stream := out["streamSettings"].(map[string]any)
			delete(stream, "sockopt")
			startTestXray(t, map[string]any{
				"log": map[string]any{"loglevel": "none"},
				"inbounds": []any{map[string]any{
					"listen": "127.0.0.1", "port": serverPort, "protocol": "vless",
					"settings":       map[string]any{"decryption": "none", "clients": []any{map[string]any{"id": integrationUUID}}},
					"streamSettings": stream,
				}},
				"outbounds": []any{map[string]any{"protocol": "freedom", "tag": "exit", "settings": map[string]any{
					"finalRules": []any{map[string]any{"action": "allow", "ip": []string{"127.0.0.0/8"}}},
				}}},
			}, false)
			clientPort := unusedTCPPort(t)
			config, err := xrayBaseConfig(srv)
			if err != nil {
				t.Fatal(err)
			}
			config["inbounds"] = []any{xraySOCKSInbound(clientPort)}
			config["routing"].(map[string]any)["rules"] = append([]map[string]any{
				{"type": "field", "domain": []string{"full:localhost"}, "outboundTag": "direct"},
			}, config["routing"].(map[string]any)["rules"].([]map[string]any)...)
			config["log"] = map[string]any{"loglevel": "debug"}
			clientCore := startTestXray(t, config, true)

			var payloadBuilder strings.Builder
			for i := 0; i < 4096; i++ {
				fmt.Fprintf(&payloadBuilder, "%08d-test-payload\n", i)
			}
			payload := payloadBuilder.String()
			httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
				io.WriteString(w, payload)
			}))
			defer httpServer.Close()
			dialer, err := proxy.SOCKS5("tcp", fmt.Sprintf("127.0.0.1:%d", clientPort), nil, &net.Dialer{Timeout: 5 * time.Second})
			if err != nil {
				t.Fatal(err)
			}
			transport := &http.Transport{Dial: dialer.Dial, DisableKeepAlives: true}
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
			for _, url := range []string{httpServer.URL, strings.Replace(httpServer.URL, "127.0.0.1", "localhost", 1)} {
				response, err := client.Get(url)
				if err != nil {
					t.Fatal(err)
				}
				body, err := io.ReadAll(response.Body)
				response.Body.Close()
				if err != nil || string(body) != payload {
					bad := 0
					for bad < len(body) && bad < len(payload) && body[bad] == payload[bad] {
						bad++
					}
					t.Fatalf("TCP payload mismatch: bytes=%d err=%v firstBad=%d actual=%q expected=%q", len(body), err, bad, body[max(0, bad-8):min(len(body), bad+32)], payload[max(0, bad-8):min(len(payload), bad+32)])
				}
			}
			testSOCKSUDP(t, clientPort)
			deadline := time.Now().Add(time.Second)
			for {
				snapshot := clientCore.TrafficSnapshot()
				if snapshot.VPNUpload > 0 && snapshot.VPNDownload >= uint64(len(payload)) && snapshot.BypassUpload > 0 && snapshot.BypassDownload >= uint64(len(payload)) {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("traffic not accounted by route: %+v", snapshot)
				}
				time.Sleep(10 * time.Millisecond)
			}
		})
	}
}

func testSOCKSUDP(t *testing.T, socksPort int) {
	t.Helper()
	echo, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		buffer := make([]byte, 2048)
		echo.SetReadDeadline(time.Now().Add(10 * time.Second))
		n, addr, err := echo.ReadFromUDP(buffer)
		if err == nil {
			echo.WriteToUDP(buffer[:n], addr)
		}
	}()
	control, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", socksPort), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	control.SetDeadline(time.Now().Add(10 * time.Second))
	control.Write([]byte{5, 1, 0})
	reply := make([]byte, 2)
	if _, err := io.ReadFull(control, reply); err != nil || !bytes.Equal(reply, []byte{5, 0}) {
		t.Fatalf("SOCKS auth: %v %v", reply, err)
	}
	control.Write([]byte{5, 3, 0, 1, 0, 0, 0, 0, 0, 0})
	reply = make([]byte, 10)
	if _, err := io.ReadFull(control, reply); err != nil || reply[1] != 0 || reply[3] != 1 {
		t.Fatalf("UDP associate: %v %v", reply, err)
	}
	relay := &net.UDPAddr{IP: net.IP(reply[4:8]), Port: int(binary.BigEndian.Uint16(reply[8:]))}
	udp, err := net.DialUDP("udp4", nil, relay)
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	udp.SetDeadline(time.Now().Add(10 * time.Second))
	payload := []byte("real UDP through VLESS XUDP")
	packet := append([]byte{0, 0, 0, 1, 127, 0, 0, 1, 0, 0}, payload...)
	binary.BigEndian.PutUint16(packet[8:10], uint16(echo.LocalAddr().(*net.UDPAddr).Port))
	if _, err := udp.Write(packet); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 2048)
	n, err := udp.Read(buffer)
	if err != nil || n < 10 || !bytes.Equal(buffer[10:n], payload) {
		t.Fatalf("UDP payload mismatch: bytes=%d err=%v", n, err)
	}
}

func TestXrayTemporaryInstanceDoesNotReplaceLiveLogger(t *testing.T) {
	config := map[string]any{"log": map[string]any{"loglevel": "warning"}, "outbounds": []any{map[string]any{"protocol": "freedom"}}}
	mainCore := startTestXray(t, config, true)
	target := engineLogs.target.Load()
	temporary := startTestXray(t, config, false)
	temporary.Close()
	if engineLogs.target.Load() != target {
		t.Fatal("probe stole the tunnel logger")
	}
	xlog.Record(&xlog.GeneralMessage{Severity: xlog.Severity_Warning, Content: "still alive"})
	entries, _ := target.rb.Entries(0)
	if len(entries) == 0 || entries[len(entries)-1].Message != "still alive" {
		t.Fatalf("logger not active: %v", entries)
	}
	mainCore.Close()
	if engineLogs.target.Load() != nil {
		t.Fatal("closed tunnel retained logger")
	}
}

func TestXrayConcurrentProbesKeepMainTunnelWorking(t *testing.T) {
	serverPort := unusedTCPPort(t)
	srv := &VLESSServer{Address: "127.0.0.1", Port: serverPort, UUID: integrationUUID, Network: "grpc", Path: "test"}
	stream := map[string]any{"network": "grpc", "grpcSettings": map[string]any{"serviceName": "test"}}
	startTestXray(t, map[string]any{
		"inbounds": []any{map[string]any{
			"listen": "127.0.0.1", "port": serverPort, "protocol": "vless",
			"settings":       map[string]any{"decryption": "none", "clients": []any{map[string]any{"id": integrationUUID}}},
			"streamSettings": stream,
		}},
		"outbounds": []any{map[string]any{"protocol": "freedom", "settings": map[string]any{
			"finalRules": []any{map[string]any{"action": "allow", "ip": []string{"127.0.0.0/8"}}},
		}}},
	}, false)
	mainPort := unusedTCPPort(t)
	config, err := xrayBaseConfig(srv)
	if err != nil {
		t.Fatal(err)
	}
	config["inbounds"] = []any{xraySOCKSInbound(mainPort)}
	mainCore := startTestXray(t, config, true)
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }))
	defer httpServer.Close()
	const probes = 12
	var wg sync.WaitGroup
	results := make(chan PingResult, probes)
	for i := 0; i < probes; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- pingOneThroughVLESSContext(context.Background(), srv, 5*time.Second, httpServer.URL)
		}()
	}
	for i := 0; i < 3; i++ {
		result := pingViaSOCKSContext(context.Background(), srv, mainPort, 5*time.Second, httpServer.URL)
		if result.Error != "" {
			t.Fatalf("main tunnel failed during probes: %s", result.Error)
		}
	}
	wg.Wait()
	close(results)
	for result := range results {
		if result.Error != "" {
			t.Errorf("parallel probe failed: %s", result.Error)
		}
	}
	if got := mainCore.TrafficSnapshot(); got.VPNUpload == 0 || got.VPNDownload == 0 {
		t.Fatalf("main tunnel stopped accounting traffic: %+v", got)
	}
}

func TestProbeSelectedServerWithoutStartingManager(t *testing.T) {
	port := unusedTCPPort(t)
	srv := VLESSServer{ID: "selected", Name: "local test", Address: "127.0.0.1", Port: port, UUID: integrationUUID, Network: "tcp"}
	startTestXray(t, map[string]any{
		"inbounds": []any{map[string]any{
			"listen": "127.0.0.1", "port": port, "protocol": "vless",
			"settings": map[string]any{"decryption": "none", "clients": []any{map[string]any{"id": integrationUUID}}},
		}},
		"outbounds": []any{map[string]any{"protocol": "freedom", "settings": map[string]any{
			"finalRules": []any{map[string]any{"action": "allow", "ip": []string{"127.0.0.0/8"}}},
		}}},
	}, false)
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }))
	defer httpServer.Close()
	dir := t.TempDir()
	cfg := defaultConfig()
	cfg.Servers = []VLESSServer{srv}
	cfg.ActiveServer = srv.ID
	cfg.Settings.PingTestURL = httpServer.URL
	if err := saveConfig(filepath.Join(dir, "config.json"), cfg); err != nil {
		t.Fatal(err)
	}
	if err := probeSelectedServer(dir); err != nil {
		t.Fatal(err)
	}
}
