package main

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"strings"
)

const (
	// tunIface / tunAddr / tunMTU describe the TUN device sing-box creates.
	// The system stack uses a /30 so it has both a "server" address (.1)
	// and a "client" NAT address (.2) without wasting space.
	// routing.go references these same constants.
	tunIface = "tun0"
	tunAddr  = "198.18.0.1/30" // .1 = sing-box server, .2 = NAT source
	tunMTU   = 1500

	// socksHealthPort is a SOCKS5 inbound bound to localhost that the failover
	// controller uses to probe VPN health.
	socksHealthPort = 7891
)

// generateSingBoxConfig builds a sing-box config: TUN inbound (system stack)
// + VLESS outbound. LAN traffic arrives at tun0 via iptables fwmark routing
// set up by routing.go. The "system" stack processes packets entirely in
// userspace (no gVisor), handles TCP/UDP NAT, and fakes ICMP echo replies
// so LAN clients' pings appear to succeed.
//
// TUN MTU is intentionally fixed at 1500. The physical LTE/WAN interface
// negotiates its own MTU; this virtual interface carries client IP packets.
func generateSingBoxConfig(cfg *Config, srv *VLESSServer) ([]byte, error) {
	logLevel := cfg.Settings.LogLevel
	if logLevel == "" {
		logLevel = "error"
	}
	// TUN inbound: sing-box creates tun0 and reads raw IP packets.
	// system stack = pure-Go userspace NAT; no iptables internals,
	// no gVisor memory overhead — right for a 57 MB MIPS router.
	// auto_route=false: we manage ip-rule routing ourselves in routing.go.
	inboundTun := map[string]any{
		"type":           "tun",
		"tag":            "tun-in",
		"interface_name": tunIface,
		"address":        []string{tunAddr},
		"mtu":            tunMTU,
		"stack":          "system",
	}

	// socks inbound: localhost-only, used by vpnProbe in failover.go.
	inboundSocks := map[string]any{
		"type":        "socks",
		"tag":         "socks-health",
		"listen":      "127.0.0.1",
		"listen_port": socksHealthPort,
	}

	proxyOutbounds, err := buildSingBoxProxyOutbounds(srv)
	if err != nil {
		return nil, err
	}

	// `direct` outbound must carry the WAN-fwmark too. Without it, sing-box
	// opens raw sockets that the OUTPUT mangle chain re-marks with 0x1 and
	// kicks back into tun0 — domain-bypass would silently loop.
	directOut := map[string]any{"type": "direct", "tag": "direct"}
	if runtime.GOOS == "linux" {
		directOut["routing_mark"] = WANFwmark
	}
	outbounds := append(proxyOutbounds, directOut)

	// DNS — local-only. Routing DNS through the VLESS tunnel on softfloat
	// MIPS causes a death spiral: every new TCP connection triggers a
	// sing-box DNS resolve through the tunnel; if the tunnel is flaky the
	// resolve times out, spawning retries, which spawn more DNS lookups…
	// kernel sys CPU hits 85 %, sshd can't fork, router watchdog fires.
	//
	// LAN clients' DNS (port 53) is bypassed in routing.go's mangle chain
	// before it even reaches tun0, so dnsmasq handles it directly.
	// Sing-box itself uses the router's resolv.conf for its own lookups.
	dns := map[string]any{
		"servers": []map[string]any{
			{"tag": "dns_local", "type": "local"},
		},
		"final":    "dns_local",
		"strategy": "ipv4_only",
	}

	rules := []map[string]any{
		// Sniff TLS SNI / HTTP Host so domain rules work without a DNS
		// lookup. Cheap, no extra connections.
		{"action": "sniff"},
	}
	// Domain-based bypass list: RU operator whitelist + user additions →
	// `direct`. Placed BEFORE the private-CIDR rule because matching is
	// first-hit. Empty list ⇒ rule is skipped, keeping the config slim
	// when the user disables it.
	if bypass := bypassDomainsFor(cfg); len(bypass) > 0 {
		rules = append(rules, map[string]any{
			"domain_suffix": bypass,
			"outbound":      "direct",
		})
	}
	// Private CIDRs — keep direct (already RETURN'd by mangle chain, but
	// defensive in case anything slips through).
	rules = append(rules, map[string]any{
		"ip_cidr": []string{
			"192.168.0.0/16",
			"10.0.0.0/8",
			"172.16.0.0/12",
			"127.0.0.0/8",
			"169.254.0.0/16",
			"224.0.0.0/4",
		},
		"outbound": "direct",
	})

	route := map[string]any{
		"rules":                   rules,
		"final":                   "proxy",
		"default_domain_resolver": "dns_local",
	}

	config := map[string]any{
		"log": map[string]any{
			"level":     logLevel,
			"timestamp": false,
			"output":    os.DevNull,
		},
		"dns":       dns,
		"inbounds":  []any{inboundTun, inboundSocks},
		"outbounds": outbounds,
		"route":     route,
	}

	return json.MarshalIndent(config, "", "  ")
}

func buildSingBoxProxyOutbounds(srv *VLESSServer) ([]map[string]any, error) {
	if len(srv.Members) == 0 {
		out, err := buildSingBoxVLESSOutbound(srv)
		if err != nil {
			return nil, err
		}
		return []map[string]any{out}, nil
	}

	outbounds := make([]map[string]any, 0, len(srv.Members)+1)
	tags := make([]string, 0, len(srv.Members))
	for i := range srv.Members {
		out, err := buildSingBoxVLESSOutbound(&srv.Members[i])
		if err != nil {
			return nil, fmt.Errorf("profile member %d: %w", i+1, err)
		}
		tag := fmt.Sprintf("proxy-%d", i+1)
		out["tag"] = tag
		tags = append(tags, tag)
		outbounds = append(outbounds, out)
	}
	outbounds = append(outbounds, map[string]any{
		"type":                        "urltest",
		"tag":                         "proxy",
		"outbounds":                   tags,
		"url":                         defaultPingTestURL,
		"interval":                    "10m",
		"tolerance":                   50,
		"idle_timeout":                "30m",
		"interrupt_exist_connections": false,
	})
	return outbounds, nil
}

func buildSingBoxVLESSOutbound(srv *VLESSServer) (map[string]any, error) {
	out := map[string]any{
		"type":        "vless",
		"tag":         "proxy",
		"server":      srv.Address,
		"server_port": srv.Port,
		"uuid":        srv.UUID,
		// A bad CDN/WS edge must fail quickly. Without an explicit cap,
		// browsers can pile up hundreds of pending handshakes on a small
		// router before the periodic health check replaces the server.
		"connect_timeout": "4s",
	}
	if runtime.GOOS == "linux" {
		// Mark sing-box's own outbound sockets so the OUTPUT mangle chain
		// returns them to the main WAN route instead of routing them back
		// into tun0. This is critical for domain/CDN VLESS hosts, where the
		// actual dialed IP may differ from the IPs resolved by routing.go.
		out["routing_mark"] = WANFwmark
	}
	if srv.Flow != "" {
		out["flow"] = srv.Flow
	}
	if srv.PacketEncoding != "" {
		out["packet_encoding"] = srv.PacketEncoding
	}

	alpnList := splitCSV(srv.ALPN)
	switch srv.Security {
	case "reality":
		tls := map[string]any{
			"enabled":     true,
			"server_name": srv.SNI,
			"utls": map[string]any{
				"enabled":     true,
				"fingerprint": orDefault(srv.Fingerprint, "chrome"),
			},
			"reality": map[string]any{
				"enabled":    true,
				"public_key": srv.PublicKey,
				"short_id":   srv.ShortID,
			},
		}
		if len(alpnList) > 0 {
			tls["alpn"] = alpnList
		}
		out["tls"] = tls
	case "tls":
		tls := map[string]any{
			"enabled":     true,
			"server_name": srv.SNI,
		}
		if len(alpnList) > 0 {
			tls["alpn"] = alpnList
		}
		if srv.Fingerprint != "" {
			tls["utls"] = map[string]any{
				"enabled":     true,
				"fingerprint": srv.Fingerprint,
			}
		}
		out["tls"] = tls
	}

	switch normalizeVLESSNetwork(srv.Network) {
	case "ws":
		t := map[string]any{"type": "ws"}
		if srv.Path != "" {
			t["path"] = srv.Path
		}
		if srv.Host != "" {
			t["headers"] = map[string]string{"Host": srv.Host}
		}
		out["transport"] = t
	case "grpc":
		out["transport"] = map[string]any{
			"type":         "grpc",
			"service_name": srv.Path,
		}
	case "h2", "http":
		t := map[string]any{"type": "http"}
		if srv.Host != "" {
			t["host"] = []string{srv.Host}
		}
		if srv.Path != "" {
			t["path"] = srv.Path
		}
		out["transport"] = t
	case "httpupgrade":
		t := map[string]any{"type": "httpupgrade"}
		if srv.Host != "" {
			t["host"] = srv.Host
		}
		if srv.Path != "" {
			t["path"] = srv.Path
		}
		out["transport"] = t
	case "quic":
		out["transport"] = map[string]any{"type": "quic"}
	default:
		if !isSupportedServer(srv) {
			return nil, fmt.Errorf("transport %s is not supported by bundled sing-box %s", srv.Network, BundledSingBox)
		}
	}

	return out, nil
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func camelToSnake(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 4)
	for i, r := range s {
		if i > 0 && r >= 'A' && r <= 'Z' {
			prev := rune(s[i-1])
			if prev < 'A' || prev > 'Z' {
				b.WriteByte('_')
			}
		}
		if r >= 'A' && r <= 'Z' {
			r += 'a' - 'A'
		}
		b.WriteRune(r)
	}
	return b.String()
}

func splitCSV(value string) []string {
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
