package main

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"time"
)

var lanIfaces = []string{"br0", "br-lan"}

const (
	// vlessMangleChain holds per-destination bypass rules and the final
	// MARK action that sends LAN and local OUTPUT traffic into tun0.
	vlessMangleChain = "VLESS_TPROXY"
	// collisions with WANFwmark (0x9911).
	tunFwmark  = 0x1
	tunRtTable = "100"

	// WANFwmark is set on sockets by wanDialer (health.go).
	WANFwmark = 0x9911
)

func chooseLanIface() string {
	for _, iface := range lanIfaces {
		if _, err := net.InterfaceByName(iface); err == nil {
			return iface
		}
	}
	return lanIfaces[0]
}

// privateCIDRs — never route through the tunnel.
var privateCIDRs = []string{
	"192.168.0.0/16",
	"10.0.0.0/8",
	"172.16.0.0/12",
	"127.0.0.0/8",
	"169.254.0.0/16",
	"224.0.0.0/4",
}

// AddPingBypass / ClearPingBypasses are no-ops in the current design: the
// router's own outbound traffic is handled in OUTPUT, not PREROUTING, and
// health probes bypass the tunnel by setting a socket mark.
func AddPingBypass(_ string) {}
func ClearPingBypasses()     {}

// ResolveAddrs returns IPv4 addresses for host. 3-second timeout.
func ResolveAddrs(host string) []string {
	if ip := net.ParseIP(host); ip != nil {
		if ip4 := ip.To4(); ip4 != nil {
			return []string{ip4.String()}
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip4", host)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(ips))
	for _, ip := range ips {
		if ip4 := ip.To4(); ip4 != nil {
			out = append(out, ip4.String())
		}
	}
	return out
}

// waitForTun polls until tun0 appears (sing-box creates it on Start) or
// the deadline passes. Returns an error if the interface never shows up.
func waitForTun(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ifaces, err := net.Interfaces(); err == nil {
			for _, iface := range ifaces {
				if iface.Name == tunIface {
					return nil
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("tun interface %s did not appear within %v", tunIface, timeout)
}

// EnableGlobalRoute routes all non-local traffic from LAN and local
// router processes through tun0:
//
//  1. VLESS_TPROXY mangle chain: RETURN private CIDRs, DNS (port 53),
//     QUIC (UDP 443), and the VLESS server IP; MARK everything else 0x1.
//  2. ip rule: fwmark 0x1 → table 100 → default dev tun0.
//     ip rule: fwmark 0x9911 → main → WAN (health/socket bypass).
//  3. iptables FORWARD: br0 ↔ tun0 ACCEPT (Keenetic default is DROP).
//
// sing-box's "system" stack handles TCP/UDP via userspace NAT and answers
// ICMP echo requests directly (fake reply from tun0), so LAN pings succeed.
func EnableGlobalRoute(vlessHost string) error {
	DisableGlobalRoute()
	applied := false
	defer func() {
		if !applied {
			DisableGlobalRoute()
		}
	}()

	// Wait for sing-box to bring up tun0 before we reference it in ip route.
	if err := waitForTun(10 * time.Second); err != nil {
		return fmt.Errorf("waiting for %s: %v", tunIface, err)
	}

	markExact := fmt.Sprintf("0x%x", tunFwmark)
	mark := fmt.Sprintf("0x%x/0xffffffff", tunFwmark)
	wanMarkExact := fmt.Sprintf("0x%x", WANFwmark)

	// ip rule + route: fwmark'd packets → table 100 → tun0.
	if err := requireRouteCommand("add tunnel policy rule", "ip", "rule", "add", "fwmark", markExact, "lookup", tunRtTable, "pref", "1"); err != nil {
		return err
	}
	if err := requireRouteCommand("add WAN bypass rule", "ip", "rule", "add", "fwmark", wanMarkExact, "lookup", "main", "pref", "2"); err != nil {
		return err
	}
	if err := requireRouteCommand("add tunnel route", "ip", "route", "add", "table", tunRtTable, "default", "dev", tunIface); err != nil {
		return err
	}

	// VLESS_TPROXY chain: bypass rules then MARK.
	if err := requireRouteCommand("create mangle chain", "iptables", "-t", "mangle", "-N", vlessMangleChain); err != nil {
		return err
	}

	for _, cidr := range privateCIDRs {
		if err := requireRouteCommand("add private network bypass", "iptables", "-t", "mangle", "-A", vlessMangleChain, "-d", cidr, "-j", "RETURN"); err != nil {
			return err
		}
	}
	if err := requireRouteCommand("add marked socket bypass", "iptables", "-t", "mangle", "-A", vlessMangleChain, "-m", "mark", "--mark", wanMarkExact, "-j", "RETURN"); err != nil {
		return err
	}

	// DNS — bypass so LAN clients' queries go straight to dnsmasq.
	// Routing DNS through tun0 causes goroutine/FD pile-up under load on MIPS.
	if err := requireRouteCommand("add UDP DNS bypass", "iptables", "-t", "mangle", "-A", vlessMangleChain, "-p", "udp", "--dport", "53", "-j", "RETURN"); err != nil {
		return err
	}
	if err := requireRouteCommand("add TCP DNS bypass", "iptables", "-t", "mangle", "-A", vlessMangleChain, "-p", "tcp", "--dport", "53", "-j", "RETURN"); err != nil {
		return err
	}

	// QUIC — drop so browsers fall back to TCP/443 through the tunnel.
	// UDP-over-VLESS is expensive; HTTP/3 is not needed for bypass to work.
	if err := requireRouteCommand("add QUIC fallback rule", "iptables", "-t", "mangle", "-A", vlessMangleChain, "-p", "udp", "--dport", "443", "-j", "DROP"); err != nil {
		return err
	}

	// VLESS server — must not enter tun0 or we get a routing loop.
	serverAddrs := ResolveAddrs(vlessHost)
	if len(serverAddrs) == 0 {
		return fmt.Errorf("resolve VLESS server %q: no IPv4 addresses", vlessHost)
	}
	for _, ip := range serverAddrs {
		if err := requireRouteCommand("add VLESS endpoint bypass", "iptables", "-t", "mangle", "-A", vlessMangleChain, "-d", ip+"/32", "-j", "RETURN"); err != nil {
			return err
		}
	}

	// Everything else from LAN or local OUTPUT gets marked → routed to tun0.
	if err := requireRouteCommand("add tunnel mark", "iptables", "-t", "mangle", "-A", vlessMangleChain, "-j", "MARK", "--set-mark", mark); err != nil {
		return err
	}

	lanIface := chooseLanIface()
	// Apply chain to traffic arriving on the LAN bridge.
	if err := requireRouteCommand("attach LAN prerouting chain", "iptables", "-t", "mangle", "-A", "PREROUTING",
		"-i", lanIface, "-j", vlessMangleChain); err != nil {
		return err
	}
	if err := requireRouteCommand("attach router output chain", "iptables", "-t", "mangle", "-A", "OUTPUT", "-j", vlessMangleChain); err != nil {
		return err
	}

	// INPUT chain: Keenetic defaults to DROP. The system stack rewrites client
	// TCP/UDP packets (src=LAN_IP, dst=external) into (src=198.18.0.2,
	// dst=198.18.0.1:listener_port) and writes them back to tun0. The kernel
	// then delivers them via INPUT to sing-box's local TCP listener. Without
	// this rule those rewritten packets are silently dropped (INPUT policy
	// DROP), so the system stack's TCP listener never receives connections.
	if err := requireRouteCommand("allow tunnel input", "iptables", "-I", "INPUT", "1", "-i", tunIface, "-j", "ACCEPT"); err != nil {
		return err
	}

	// FORWARD chain: Keenetic defaults to DROP; allow LAN ↔ tun0 traffic.
	// br0/br-lan → tun0: LAN client packets going into sing-box.
	// tun0 → (any): sing-box reply packets going back to LAN clients.
	if err := requireRouteCommand("allow LAN tunnel forwarding", "iptables", "-A", "FORWARD", "-i", lanIface, "-o", tunIface, "-j", "ACCEPT"); err != nil {
		return err
	}
	if err := requireRouteCommand("allow tunnel return forwarding", "iptables", "-A", "FORWARD", "-i", tunIface, "-j", "ACCEPT"); err != nil {
		return err
	}

	// POSTROUTING nat: Keenetic's _NDM_MASQ rule automatically masquerades
	// all LAN (192.168.201.x) traffic exiting on any non-br0 interface,
	// including tun0. Without this bypass, the source IP gets rewritten to
	// 198.18.0.1 (tun0 address) before sing-box reads the packet — so the
	// system-stack reply goes back to the router, not to the LAN client.
	// Insert at position 1 so it fires before _NDM_IPSEC / _NDM_MASQ.
	if err := requireRouteCommand("add tunnel NAT bypass", "iptables", "-t", "nat", "-I", "POSTROUTING", "1", "-o", tunIface, "-j", "RETURN"); err != nil {
		return err
	}

	applied = true
	return nil
}

func requireRouteCommand(stage, name string, args ...string) error {
	output, err := run(name, args...)
	if err != nil {
		return fmt.Errorf("%s: %s %v: %w (%s)", stage, name, args, err, output)
	}
	return nil
}

// GlobalRouteReady verifies the state most commonly removed by a Keenetic
// firewall reload. The endpoint bypass and mark rules live in the same custom
// chain, so an existing chain plus both attachment points and the policy route
// are sufficient for the lightweight periodic guard.
func GlobalRouteReady() bool {
	if _, err := run("iptables", "-t", "mangle", "-S", vlessMangleChain); err != nil {
		return false
	}
	if _, err := run("iptables", "-t", "mangle", "-C", "OUTPUT", "-j", vlessMangleChain); err != nil {
		return false
	}
	if _, err := run("iptables", "-t", "mangle", "-C", "PREROUTING",
		"-i", chooseLanIface(), "-j", vlessMangleChain); err != nil {
		return false
	}
	if _, err := run("ip", "route", "show", "table", tunRtTable, "default", "dev", tunIface); err != nil {
		return false
	}
	return true
}

// DisableGlobalRoute removes everything EnableGlobalRoute installed.
func DisableGlobalRoute() {
	// PREROUTING/OUTPUT jump.
	for _, iface := range lanIfaces {
		for i := 0; i < 5; i++ {
			if _, err := run("iptables", "-t", "mangle", "-D", "PREROUTING",
				"-i", iface, "-j", vlessMangleChain); err != nil {
				break
			}
		}
	}
	for i := 0; i < 5; i++ {
		if _, err := run("iptables", "-t", "mangle", "-D", "OUTPUT",
			"-j", vlessMangleChain); err != nil {
			break
		}
	}

	// Flush + delete our mangle chain.
	run("iptables", "-t", "mangle", "-F", vlessMangleChain)
	run("iptables", "-t", "mangle", "-X", vlessMangleChain)

	// FORWARD rules.
	for _, iface := range lanIfaces {
		for i := 0; i < 3; i++ {
			if _, err := run("iptables", "-D", "FORWARD", "-i", iface, "-o", tunIface, "-j", "ACCEPT"); err != nil {
				break
			}
		}
	}
	for i := 0; i < 3; i++ {
		if _, err := run("iptables", "-D", "FORWARD", "-i", tunIface, "-j", "ACCEPT"); err != nil {
			break
		}
	}

	// ip rule + route.
	// Try both the new exact form and the old "0x1/0x1" buggy-mask form
	// so an upgrade from a stale TPROXY build also cleans up.
	markExact := fmt.Sprintf("0x%x", tunFwmark)
	for _, ruleMark := range []string{markExact, fmt.Sprintf("0x%x/0x%x", tunFwmark, tunFwmark)} {
		for i := 0; i < 5; i++ {
			cmd := exec.Command("ip", "rule", "del", "fwmark", ruleMark, "lookup", tunRtTable)
			if err := cmd.Run(); err != nil {
				break
			}
		}
	}
	wanMarkExact := fmt.Sprintf("0x%x", WANFwmark)
	for i := 0; i < 5; i++ {
		cmd := exec.Command("ip", "rule", "del", "fwmark", wanMarkExact, "lookup", "main")
		if err := cmd.Run(); err != nil {
			break
		}
	}
	run("ip", "route", "flush", "table", tunRtTable)

	// INPUT rule for tun0.
	for i := 0; i < 3; i++ {
		if _, err := run("iptables", "-D", "INPUT", "-i", tunIface, "-j", "ACCEPT"); err != nil {
			break
		}
	}

	// POSTROUTING nat bypass for tun0.
	for i := 0; i < 3; i++ {
		if _, err := run("iptables", "-t", "nat", "-D", "POSTROUTING", "-o", tunIface, "-j", "RETURN"); err != nil {
			break
		}
	}

	// Legacy TPROXY-mode DIVERT chain (pre-1.5 builds). No-ops if absent.
	run("iptables", "-t", "mangle", "-D", "PREROUTING", "-p", "tcp", "-m", "socket", "-j", "VLESS_DIVERT")
	run("iptables", "-t", "mangle", "-D", "PREROUTING", "-p", "udp", "-m", "socket", "-j", "VLESS_DIVERT")
	run("iptables", "-t", "mangle", "-F", "VLESS_DIVERT")
	run("iptables", "-t", "mangle", "-X", "VLESS_DIVERT")
}
