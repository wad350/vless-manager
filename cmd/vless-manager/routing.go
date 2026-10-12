package main

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"
)

var lanIfaces = []string{"br0", "br-lan"}
var tunnelProtocols = []string{"tcp", "udp"}

const (
	vlessMangleChain = "VLESS_TPROXY"
	vlessOutputChain = "VLESS_OUTPUT"
	vlessInputChain  = "VLESS_IN"
	vlessTCPChain    = "VLESS_TCP"    // Legacy REDIRECT cleanup.
	vlessTCPInput    = "VLESS_TCP_IN" // Legacy REDIRECT cleanup.
	tunFwmark        = 0x1
	tunRtTable       = "100"
	WANFwmark        = 0x9911
)

type routeMode string

const routeTPROXY routeMode = "tproxy"

func chooseLanIface() string {
	for _, iface := range lanIfaces {
		if _, err := net.InterfaceByName(iface); err == nil {
			return iface
		}
	}
	return lanIfaces[0]
}

// Local, multicast and broadcast destinations never enter the proxy.
var privateCIDRs = []string{
	"192.168.0.0/16", "10.0.0.0/8", "172.16.0.0/12", "127.0.0.0/8",
	"169.254.0.0/16", "224.0.0.0/4", "255.255.255.255/32", "0.0.0.0/8",
}

// Keenetic may lack xt_addrtype. Exclude its public interface addresses
// explicitly; private addresses are already covered by privateCIDRs.
func localIPv4Addresses() ([]string, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil, err
	}
	var result []string
	seen := make(map[string]bool)
	for _, addr := range addrs {
		ip, _, err := net.ParseCIDR(addr.String())
		if err != nil || ip.To4() == nil {
			continue
		}
		private := false
		for _, cidr := range privateCIDRs {
			_, network, _ := net.ParseCIDR(cidr)
			if network.Contains(ip) {
				private = true
				break
			}
		}
		if value := ip.To4().String(); !private && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	return result, nil
}

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

// Only DNS/53 to the router's actual upstreams is exempted, not DoH/HTTPS.
func keeneticUpstreamDNSServers(command func(string, ...string) (string, error)) []string {
	output, err := command("ndmc", "-c", "show ip name-server")
	if err != nil {
		return nil
	}
	seen := make(map[string]bool)
	var servers []string
	for _, line := range strings.Split(output, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok || strings.TrimSpace(key) != "address" {
			continue
		}
		if ip := net.ParseIP(strings.TrimSpace(value)); ip != nil && ip.To4() != nil {
			address := ip.To4().String()
			if !seen[address] {
				seen[address] = true
				servers = append(servers, address)
			}
		}
	}
	return servers
}

// Probe an unattached chain. Keenetic ships modules but does not always
// autoload them; never change live hooks just to detect support.
func ensureTProxySupport(command func(string, ...string) (string, error)) error {
	const probe = "VLESS_PROBE"
	command("iptables", "-t", "mangle", "-F", probe)
	command("iptables", "-t", "mangle", "-X", probe)
	if output, err := command("iptables", "-t", "mangle", "-N", probe); err != nil {
		return fmt.Errorf("create TPROXY probe: %w (%s)", err, output)
	}
	defer command("iptables", "-t", "mangle", "-X", probe)
	defer command("iptables", "-t", "mangle", "-F", probe)
	for _, protocol := range tunnelProtocols {
		args := []string{"-t", "mangle", "-A", probe, "-p", protocol, "-j", "TPROXY", "--on-port", fmt.Sprint(tcpRedirectPort), "--tproxy-mark", "0x1/0xffffffff"}
		if _, err := command("iptables", args...); err == nil {
			continue
		}
		command("modprobe", "xt_TPROXY")
		if release, err := command("uname", "-r"); err == nil {
			release = strings.TrimSpace(release)
			if release != "" && !strings.ContainsAny(release, "/\\\n\r") {
				command("insmod", "/lib/modules/"+release+"/xt_TPROXY.ko")
			}
		}
		if output, err := command("iptables", args...); err != nil {
			return fmt.Errorf("kernel TPROXY %s: %w (%s)", protocol, err, output)
		}
	}
	return nil
}

func EnableGlobalRoute(vlessHost string) error {
	serverAddrs := ResolveAddrs(vlessHost)
	if len(serverAddrs) == 0 {
		return fmt.Errorf("resolve VPN server %q: no IPv4 addresses", vlessHost)
	}
	localIPs, err := localIPv4Addresses()
	if err != nil {
		return fmt.Errorf("read local IPv4 addresses: %w", err)
	}
	DisableGlobalRoute()
	plan := globalRouteCommands(chooseLanIface(), keeneticUpstreamDNSServers(run), serverAddrs, localIPs)
	return installGlobalRoute(plan, run, DisableGlobalRoute)
}

// TCP and UDP use the native kernel stack. OUTPUT marks router traffic for
// loopback delivery; PREROUTING hands LAN and loopback packets to Xray.
func globalRouteCommands(lanIface string, dnsServers, endpointIPs, localIPs []string) [][]string {
	plan := [][]string{
		{"ip", "rule", "add", "fwmark", "0x1", "lookup", tunRtTable, "pref", "1"},
		{"ip", "rule", "add", "fwmark", "0x9911", "lookup", "main", "pref", "2"},
		{"ip", "route", "add", "local", "0.0.0.0/0", "dev", "lo", "table", tunRtTable},
	}
	add := func(args ...string) { plan = append(plan, args) }
	for _, chain := range []string{vlessMangleChain, vlessOutputChain} {
		add("iptables", "-t", "mangle", "-N", chain)
		add("iptables", "-t", "mangle", "-A", chain, "-m", "mark", "--mark", "0x9911", "-j", "RETURN")
		for _, cidr := range privateCIDRs {
			add("iptables", "-t", "mangle", "-A", chain, "-d", cidr, "-j", "RETURN")
		}
		for _, ip := range append(append([]string{}, endpointIPs...), localIPs...) {
			add("iptables", "-t", "mangle", "-A", chain, "-d", ip+"/32", "-j", "RETURN")
		}
		for _, ip := range dnsServers {
			for _, protocol := range tunnelProtocols {
				add("iptables", "-t", "mangle", "-A", chain, "-d", ip+"/32", "-p", protocol, "--dport", "53", "-j", "RETURN")
			}
		}
		for _, protocol := range tunnelProtocols {
			if chain == vlessMangleChain {
				add("iptables", "-t", "mangle", "-A", chain, "-p", protocol, "-j", "TPROXY", "--on-port", fmt.Sprint(tcpRedirectPort), "--on-ip", "127.0.0.1", "--tproxy-mark", "0x1/0xffffffff")
			} else {
				add("iptables", "-t", "mangle", "-A", chain, "-p", protocol, "-j", "MARK", "--set-mark", "0x1/0xffffffff")
			}
		}
	}
	// Keenetic's mangle INPUT inspects port 443 as its own HTTPS console and
	// drops proxied TLS after the handshake. Only our captured LAN/lo packets
	// must bypass that inspection; filter INPUT still protects the listener.
	add("iptables", "-t", "mangle", "-N", vlessInputChain)
	for _, iface := range []string{lanIface, "lo"} {
		add("iptables", "-t", "mangle", "-A", vlessInputChain, "-i", iface, "-m", "mark", "--mark", "0x1", "-j", "ACCEPT")
	}
	add("iptables", "-t", "mangle", "-A", vlessInputChain, "-j", "RETURN")
	add("iptables", "-N", vlessInputChain)
	for _, iface := range []string{lanIface, "lo"} {
		add("iptables", "-A", vlessInputChain, "-i", iface, "-m", "mark", "--mark", "0x1", "-j", "ACCEPT")
	}
	// The listener is not a publicly accessible forward proxy. Intercepted
	// packets retain their original destination/port and carry our mark.
	for _, protocol := range tunnelProtocols {
		add("iptables", "-A", vlessInputChain, "-p", protocol, "--dport", fmt.Sprint(tcpRedirectPort), "-j", "DROP")
	}
	add("iptables", "-A", vlessInputChain, "-j", "RETURN")
	add("iptables", "-I", "INPUT", "1", "-j", vlessInputChain)
	add("iptables", "-t", "mangle", "-I", "INPUT", "1", "-j", vlessInputChain)
	add("iptables", "-t", "mangle", "-I", "PREROUTING", "1", "-i", "lo", "-j", vlessMangleChain)
	add("iptables", "-t", "mangle", "-I", "PREROUTING", "1", "-i", lanIface, "-j", vlessMangleChain)
	add("iptables", "-t", "mangle", "-I", "OUTPUT", "1", "-j", vlessOutputChain)
	return plan
}

func installGlobalRoute(plan [][]string, command func(string, ...string) (string, error), cleanup func()) error {
	for _, args := range plan {
		if output, err := command(args[0], args[1:]...); err != nil {
			cleanup()
			return fmt.Errorf("install route %s: %w (%s)", strings.Join(args, " "), err, output)
		}
	}
	return nil
}

func requireRouteCommand(stage, name string, args ...string) error {
	output, err := run(name, args...)
	if err != nil {
		return fmt.Errorf("%s: %s %v: %w (%s)", stage, name, args, err, output)
	}
	return nil
}

func GlobalRouteReady() bool { return globalRouteReady(run) }

// Four command snapshots instead of dozens of iptables forks on small MIPS.
func globalRouteReady(command func(string, ...string) (string, error)) bool {
	mangle, err := command("iptables", "-t", "mangle", "-S")
	if err != nil {
		return false
	}
	filter, err := command("iptables", "-S")
	if err != nil {
		return false
	}
	localIPs, err := localIPv4Addresses()
	if err != nil {
		return false
	}
	// Reuse the installer to check bypasses, both protocols and protection.
	for _, args := range globalRouteCommands(chooseLanIface(), nil, nil, localIPs) {
		if args[0] != "iptables" {
			continue
		}
		snapshot, rule := filter, args[1:]
		if len(rule) > 2 && rule[0] == "-t" {
			snapshot, rule = mangle, rule[2:]
		}
		if rule[0] == "-I" {
			rule = append([]string{"-A", rule[1]}, rule[3:]...)
		}
		if !iptablesSnapshotHasRule(snapshot, rule) {
			return false
		}
	}
	if !firstChainRuleMatches(mangle, []string{"OUTPUT", "-j", vlessOutputChain}) ||
		!firstChainRuleMatches(mangle, []string{"PREROUTING", "-i", chooseLanIface(), "-j", vlessMangleChain}) ||
		!firstChainRuleMatches(mangle, []string{"INPUT", "-j", vlessInputChain}) ||
		!firstChainRuleMatches(filter, []string{"INPUT", "-j", vlessInputChain}) {
		return false
	}
	rules, err := command("ip", "rule", "show")
	if err != nil || !outputLineHasTokens(rules, "fwmark", "0x1", "lookup", tunRtTable) ||
		!outputLineHasTokens(rules, "fwmark", "0x9911", "lookup", "main") {
		return false
	}
	routes, err := command("ip", "route", "show", "table", tunRtTable)
	return err == nil && (outputLineHasTokens(routes, "local", "default", "dev", "lo") ||
		outputLineHasTokens(routes, "local", "0.0.0.0/0", "dev", "lo"))
}

func iptablesSnapshotHasRule(snapshot string, rule []string) bool {
	// iptables adds an implicit protocol match when printing --dport.
	key := func(fields []string) string {
		var result []string
		for i := 0; i < len(fields); i++ {
			if fields[i] == "-m" && i+1 < len(fields) && (fields[i+1] == "tcp" || fields[i+1] == "udp") {
				i++
				continue
			}
			result = append(result, fields[i])
			if fields[i] == "--set-mark" {
				result[len(result)-1] = "--set-xmark"
			}
		}
		return strings.Join(result, " ")
	}
	want := key(rule)
	for _, line := range strings.Split(snapshot, "\n") {
		if key(strings.Fields(line)) == want {
			return true
		}
	}
	return false
}

func firstChainRuleMatches(output string, hook []string) bool {
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) > 1 && fields[0] == "-A" && fields[1] == hook[0] {
			return strings.Join(fields[1:], " ") == strings.Join(hook, " ")
		}
	}
	return false
}

func outputLineHasTokens(output string, required ...string) bool {
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		matched := true
		for _, token := range required {
			found := false
			for _, field := range fields {
				if field == token {
					found = true
					break
				}
			}
			if !found {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

func DisableGlobalRoute() { disableGlobalRoute(run) }

func disableGlobalRoute(command func(string, ...string) (string, error)) {
	remove := func(name string, args ...string) {
		for i := 0; i < 5; i++ {
			if _, err := command(name, args...); err != nil {
				break
			}
		}
	}
	// Detach hooks before removing policy routes, including stale old builds.
	remove("iptables", "-t", "mangle", "-D", "OUTPUT", "-j", vlessOutputChain)
	remove("iptables", "-t", "mangle", "-D", "OUTPUT", "-j", vlessMangleChain)
	for _, iface := range append(append([]string{}, lanIfaces...), "lo") {
		remove("iptables", "-t", "mangle", "-D", "PREROUTING", "-i", iface, "-j", vlessMangleChain)
	}
	remove("iptables", "-D", "INPUT", "-j", vlessInputChain)
	remove("iptables", "-t", "mangle", "-D", "INPUT", "-j", vlessInputChain)
	for _, chain := range []string{vlessMangleChain, vlessOutputChain, vlessInputChain, "VLESS_PROBE"} {
		command("iptables", "-t", "mangle", "-F", chain)
		command("iptables", "-t", "mangle", "-X", chain)
	}
	command("iptables", "-F", vlessInputChain)
	command("iptables", "-X", vlessInputChain)
	for _, mark := range []string{"0x1", "0x1/0x1"} {
		remove("ip", "rule", "del", "fwmark", mark, "lookup", tunRtTable)
	}
	remove("ip", "rule", "del", "fwmark", "0x9911", "lookup", "main")
	command("ip", "route", "flush", "table", tunRtTable)
	// Legacy TUN and TCP REDIRECT state. Never flush Keenetic's chains.
	for _, iface := range lanIfaces {
		remove("iptables", "-t", "nat", "-D", "PREROUTING", "-i", iface, "-p", "tcp", "-j", vlessTCPChain)
		remove("iptables", "-D", "FORWARD", "-i", iface, "-o", tunIface, "-j", "ACCEPT")
	}
	remove("iptables", "-t", "nat", "-D", "OUTPUT", "-p", "tcp", "-j", vlessTCPChain)
	remove("iptables", "-D", "INPUT", "-p", "tcp", "--dport", fmt.Sprint(tcpRedirectPort), "-j", vlessTCPInput)
	command("iptables", "-t", "nat", "-F", vlessTCPChain)
	command("iptables", "-t", "nat", "-X", vlessTCPChain)
	command("iptables", "-F", vlessTCPInput)
	command("iptables", "-X", vlessTCPInput)
	remove("iptables", "-D", "FORWARD", "-i", tunIface, "-j", "ACCEPT")
	remove("iptables", "-D", "INPUT", "-i", tunIface, "-j", "ACCEPT")
	remove("iptables", "-t", "nat", "-D", "POSTROUTING", "-o", tunIface, "-j", "RETURN")
	for _, protocol := range tunnelProtocols {
		remove("iptables", "-t", "mangle", "-D", "PREROUTING", "-p", protocol, "-m", "socket", "-j", "VLESS_DIVERT")
	}
	command("iptables", "-t", "mangle", "-F", "VLESS_DIVERT")
	command("iptables", "-t", "mangle", "-X", "VLESS_DIVERT")
}
