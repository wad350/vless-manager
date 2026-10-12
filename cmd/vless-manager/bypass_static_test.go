package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/features/routing"
	routingSession "github.com/xtls/xray-core/features/routing/session"
	"golang.org/x/net/idna"
)

func TestBypassSourceRejectsUnsafeAndTruncatedLists(t *testing.T) {
	domains := make([]string, 100)
	for i := range domains {
		domains[i] = fmt.Sprintf("site%d.example.org", i)
	}
	for _, tail := range []string{"ru", "full:example.org", "192.168.1.1", "*.example.org", "<html>"} {
		t.Run(tail, func(t *testing.T) {
			client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(strings.Join(domains, "\n") + "\n" + tail))}, nil
			})}
			if _, err := fetchBypassDomainSource(context.Background(), client, bypassWhitelistURL); err == nil {
				t.Fatal("unsafe source accepted")
			}
		})
	}
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(strings.Join(domains, "\n") + strings.Repeat("\n", 2<<20)))}, nil
	})}
	if _, err := fetchBypassWhitelist(client); err == nil {
		t.Fatal("oversized source accepted")
	}
}

func TestBypassSourceNormalizesIDNBeforeDeduplication(t *testing.T) {
	domains := make([]string, 100)
	for i := range domains {
		domains[i] = fmt.Sprintf("site%d.example.org", i)
	}
	unicodeName := "\u044f\u043d\u0434\u0435\u043a\u0441.\u0440\u0444"
	asciiName, err := idna.Lookup.ToASCII(unicodeName)
	if err != nil {
		t.Fatal(err)
	}
	domains = append(domains, unicodeName, asciiName)
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(strings.Join(domains, "\n")))}, nil
	})}
	got, err := fetchBypassDomainSource(context.Background(), client, bypassWhitelistURL)
	if err != nil || len(got) != 101 || !slices.Contains(got, asciiName) || slices.Contains(got, unicodeName) {
		t.Fatalf("IDN source: %v, %v", got, err)
	}
}

func TestBypassCurrentSourceOptIn(t *testing.T) {
	if os.Getenv("VLESS_TEST_BYPASS_SOURCES") != "1" {
		t.Skip("live upstream download is opt-in")
	}
	base, err := fetchBypassDomainSource(context.Background(), &http.Client{Timeout: 20 * time.Second}, bypassWhitelistURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("live source: %d domains", len(base))
}

func TestBypassWANClientNeverUsesProxyOrPrivateDestinations(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:7891")
	client := bypassWANHTTPClient(time.Second)
	transport := client.Transport.(*http.Transport)
	if transport.Proxy != nil || transport.DialContext == nil || transport.TLSClientConfig != nil && transport.TLSClientConfig.InsecureSkipVerify {
		t.Fatal("source client can use proxy or ignore TLS certificates")
	}
	if _, err := transport.DialContext(context.Background(), "tcp", "127.0.0.1:443"); err == nil {
		t.Fatal("private DNS answer accepted")
	}
	for _, address := range []string{"10.1.2.3", "192.168.13.1", "169.254.1.2", "100.64.1.2", "198.18.1.2", "0.1.2.3", "224.0.0.1", "255.255.255.255", "::1"} {
		if bypassPublicIPv4(netip.MustParseAddr(address)) {
			t.Errorf("non-public target accepted: %s", address)
		}
	}
}

func TestBypassRefreshFetchesOneFileAndAvoidsFlashRewrite(t *testing.T) {
	api := newHandlerTestAPI(t)
	var calls int
	api.bypassFetch = func(_ context.Context, source string) ([]string, string, error) {
		calls++
		if source != bypassWhitelistURL {
			t.Fatalf("unexpected source: %s", source)
		}
		return []string{"base.example.org"}, "vless", nil
	}
	status, err := api.refreshBypassLists(context.Background(), "manual")
	if err != nil || calls != 1 || status.Count != 1 || status.EffectiveCount != 1 {
		t.Fatalf("refresh: %+v, calls=%d, %v", status, calls, err)
	}
	first, _ := os.Stat(api.cfgPath)
	firstData, _ := os.ReadFile(api.cfgPath)
	_, err = api.refreshBypassLists(context.Background(), "manual")
	if err != nil || calls != 2 {
		t.Fatalf("refresh: calls=%d, %v", calls, err)
	}
	second, _ := os.Stat(api.cfgPath)
	secondData, _ := os.ReadFile(api.cfgPath)
	if !os.SameFile(first, second) || !first.ModTime().Equal(second.ModTime()) || string(firstData) != string(secondData) {
		t.Fatal("unchanged source rewrote flash")
	}
}

func TestBypassRefreshKeepsPreviousCacheOnBaseOrWriteFailure(t *testing.T) {
	api := newHandlerTestAPI(t)
	api.cfg.BypassCache.Domains = []string{"old.example.org"}
	api.bypassFetch = func(context.Context, string) ([]string, string, error) { return nil, "wan", errors.New("offline") }
	if _, err := api.refreshBypassLists(context.Background(), "manual"); err == nil || api.cfg.BypassCache.Domains[0] != "old.example.org" {
		t.Fatal("failed download replaced working cache")
	}
	api.bypassFetch = func(context.Context, string) ([]string, string, error) {
		return []string{"new.example.org"}, "wan", nil
	}
	api.cfgPath = filepath.Join(api.cfgPath, "invalid.json")
	if err := os.WriteFile(filepath.Dir(api.cfgPath), []byte("block parent directory"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := api.refreshBypassLists(context.Background(), "manual"); err == nil || api.cfg.BypassCache.Domains[0] != "old.example.org" {
		t.Fatal("failed persistence replaced memory cache")
	}
}

func TestLiveBypassRulesUpdateWithoutRestart(t *testing.T) {
	cfg := defaultConfig()
	cfg.BypassCache.Domains = []string{"base.example.org"}
	data, err := generateXrayConfig(cfg, &VLESSServer{Address: "127.0.0.1", Port: 443, UUID: integrationUUID})
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	config["inbounds"] = []any{xraySOCKSInbound(unusedTCPPort(t))}
	engine := startTestXray(t, config, false).(*embeddedXray)
	router := engine.GetFeature(routing.RouterType()).(routing.Router)
	pm := NewProcessManager(t.TempDir())
	pm.box, pm.running, pm.startedAt = engine, true, time.Now()
	started := pm.startedAt
	check := func(domain string, network xnet.Network, port xnet.Port, inbound, want string) {
		t.Helper()
		route, err := router.PickRoute(&routingSession.Context{
			Inbound:  &session.Inbound{Tag: inbound},
			Outbound: &session.Outbound{Target: xnet.Destination{Address: xnet.ParseAddress(domain), Network: network, Port: port}},
		})
		if err != nil || route.GetOutboundTag() != want {
			t.Fatalf("%s/%s:%d inbound=%s route=%v, err=%v, want %s", domain, network, port, inbound, route, err, want)
		}
	}
	check("new.example.org", xnet.Network_TCP, 443, "tproxy-in", "proxy")
	cfg.BypassCache.Domains = append(cfg.BypassCache.Domains, "new.example.org")
	if err := pm.UpdateBypassRules(cfg); err != nil {
		t.Fatal(err)
	}
	check("new.example.org", xnet.Network_TCP, 443, "tproxy-in", "direct")
	check("child.new.example.org", xnet.Network_TCP, 443, "tproxy-in", "direct")
	check("new.example.org.evil.test", xnet.Network_TCP, 443, "tproxy-in", "proxy")
	check("new.example.org", xnet.Network_UDP, 443, "tproxy-in", "direct")
	check("new.example.org", xnet.Network_TCP, 80, "tproxy-in", "direct")
	check("new.example.org", xnet.Network_TCP, 443, xrayTunnelTestTag, "proxy")
	check("child.base.example.org", xnet.Network_UDP, 443, "tproxy-in", "direct")
	check("192.168.13.1", xnet.Network_TCP, 80, "tproxy-in", "direct")
	cfg.Settings.BypassRouteRussia = false
	if err := pm.UpdateBypassRules(cfg); err != nil {
		t.Fatal(err)
	}
	check("new.example.org", xnet.Network_TCP, 443, "tproxy-in", "proxy")
	if pm.box != engine || !pm.running || !pm.startedAt.Equal(started) {
		t.Fatal("routing update restarted or replaced tunnel")
	}
}

func TestLiveBypassUpdatePreservesProfileBalancer(t *testing.T) {
	cfg := defaultConfig()
	cfg.BypassCache.Domains = []string{"base.example.org"}
	member := VLESSServer{Address: "127.0.0.1", Port: unusedTCPPort(t), UUID: integrationUUID, Network: "grpc"}
	group := member
	group.Members = []VLESSServer{member, member}
	data, err := generateXrayConfig(cfg, &group)
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	config["inbounds"] = []any{xraySOCKSInbound(unusedTCPPort(t))}
	config["observatory"] = map[string]any{"subjectSelector": []string{"proxy-"}, "probeURL": "http://127.0.0.1:1/", "probeInterval": "10m"}
	engine := startTestXray(t, config, false).(*embeddedXray)
	for _, enabled := range []bool{false, true, false} {
		cfg.Settings.BypassRouteRussia = enabled
		if enabled {
			cfg.BypassCache.Domains = append(cfg.BypassCache.Domains, "new.example.org")
		}
		if err := engine.UpdateBypassRules(cfg); err != nil {
			t.Fatal(err)
		}
		for _, inbound := range []string{xrayTunnelTestTag, "tproxy-in"} {
			domain := "notlisted.example.org"
			route, err := engine.GetFeature(routing.RouterType()).(routing.Router).PickRoute(&routingSession.Context{
				Inbound:  &session.Inbound{Tag: inbound},
				Outbound: &session.Outbound{Target: xnet.TCPDestination(xnet.DomainAddress(domain), 443)},
			})
			if err != nil || route.GetOutboundTag() != "proxy-1" {
				t.Fatalf("profile balancer lost: %v, %v", route, err)
			}
		}
	}
}

func TestStaticBypassDropsAllLegacyCandidateState(t *testing.T) {
	var cfg Config
	if err := json.Unmarshal([]byte(`{"settings":{"bypass_route_russia":true},"bypass_cache":{"domains":["base.example.org"],"https_domains":["pinned.example.org"],"candidates":["unconfirmed.example.org"],"candidates_source":"unused","excluded_domains":["base.example.org"]}}`), &cfg); err != nil {
		t.Fatal(err)
	}
	clone := cloneConfig(&cfg)
	clone.BypassCache.Domains[0] = "changed.example.org"
	if cfg.BypassCache.Domains[0] != "base.example.org" {
		t.Fatal("clone shares list memory")
	}
	data, err := json.Marshal(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, obsolete := range []string{"candidates", "unconfirmed", "pinned", "excluded"} {
		if strings.Contains(string(data), obsolete) {
			t.Fatalf("legacy state retained: %s", data)
		}
	}
	if !slices.Equal(bypassListDomainsFor(&cfg), []string{"base.example.org"}) {
		t.Fatal("main list lost or additional domains imported")
	}
	api := newHandlerTestAPI(t)
	api.cfg = &cfg
	res := apiRequest(t, api, http.MethodPut, "/api/bypass", `{"merge_verified":true}`)
	if res.Code != http.StatusMethodNotAllowed {
		t.Fatalf("obsolete merge still accepted: %d", res.Code)
	}
	res = apiRequest(t, api, http.MethodGet, "/api/bypass", "")
	if res.Code != http.StatusOK || strings.Contains(res.Body.String(), "candidate") || strings.Contains(res.Body.String(), "verified") || strings.Contains(res.Body.String(), "pinned") {
		t.Fatalf("obsolete verification exposed: %s", res.Body.String())
	}
}
