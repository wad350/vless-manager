package main

import (
	"errors"
	"strings"
	"testing"
)

func tproxyReadySnapshots(t *testing.T) [2]string {
	t.Helper()
	ips, err := localIPv4Addresses()
	if err != nil {
		t.Fatal(err)
	}
	var snapshots [2]string
	for _, args := range globalRouteCommands(chooseLanIface(), nil, nil, ips) {
		if args[0] != "iptables" {
			continue
		}
		table, rule := 1, args[1:]
		if rule[0] == "-t" {
			table, rule = 0, rule[2:]
		}
		if rule[0] == "-I" {
			line := "-A " + rule[1] + " " + strings.Join(rule[3:], " ") + "\n"
			snapshots[table] = line + snapshots[table]
		} else {
			snapshots[table] += strings.Join(rule, " ") + "\n"
		}
	}
	return snapshots
}

func TestTProxyRuleOrderingAndBypasses(t *testing.T) {
	var lines []string
	for _, args := range globalRouteCommands("br0", []string{"203.0.113.53"}, []string{"203.0.113.20"}, []string{"203.0.113.1"}) {
		lines = append(lines, strings.Join(args, " "))
	}
	index := func(want string) int {
		t.Helper()
		for i, line := range lines {
			if line == want {
				return i
			}
		}
		t.Fatalf("missing %s", want)
		return -1
	}
	index("ip route add local 0.0.0.0/0 dev lo table 100")
	for _, chain := range []string{vlessMangleChain, vlessOutputChain} {
		for _, proto := range tunnelProtocols {
			target := "-j MARK --set-mark 0x1/0xffffffff"
			if chain == vlessMangleChain {
				target = "-j TPROXY --on-port 7892 --on-ip 127.0.0.1 --tproxy-mark 0x1/0xffffffff"
			}
			capture := index("iptables -t mangle -A " + chain + " -p " + proto + " " + target)
			bypasses := []string{"-m mark --mark 0x9911", "-d 203.0.113.20/32", "-d 203.0.113.1/32", "-d 203.0.113.53/32 -p " + proto + " --dport 53"}
			for _, cidr := range privateCIDRs {
				bypasses = append(bypasses, "-d "+cidr)
			}
			for _, bypass := range bypasses {
				if index("iptables -t mangle -A "+chain+" "+bypass+" -j RETURN") >= capture {
					t.Fatal("bypass installed after capture")
				}
			}
		}
	}
	protection := index("iptables -I INPUT 1 -j VLESS_IN")
	inspectionBypass := index("iptables -t mangle -I INPUT 1 -j VLESS_IN")
	for _, iface := range []string{"br0", "lo"} {
		index("iptables -t mangle -A VLESS_IN -i " + iface + " -m mark --mark 0x1 -j ACCEPT")
	}
	for _, hook := range []string{
		"iptables -t mangle -I PREROUTING 1 -i lo -j VLESS_TPROXY",
		"iptables -t mangle -I PREROUTING 1 -i br0 -j VLESS_TPROXY",
		"iptables -t mangle -I OUTPUT 1 -j VLESS_OUTPUT",
	} {
		if index(hook) <= protection || index(hook) <= inspectionBypass {
			t.Fatal("capture installed before INPUT protection")
		}
	}
	for _, line := range lines {
		if strings.Contains(line, "tun0") || strings.Contains(line, "REDIRECT") || strings.Contains(line, "FORWARD") {
			t.Fatal("old userspace/NAT path: " + line)
		}
		if strings.Contains(line, "-d 203.0.113.53/32 -j RETURN") {
			t.Fatal("DNS bypass captures DoH")
		}
	}
}

func TestRouteInstallFailureAlwaysCleansUp(t *testing.T) {
	plan := globalRouteCommands("br0", nil, []string{"203.0.113.20"}, nil)
	for failAt := range plan {
		calls, cleaned := 0, 0
		cause := errors.New("kernel rejected rule")
		err := installGlobalRoute(plan, func(string, ...string) (string, error) {
			position := calls
			calls++
			if position == failAt {
				return "failure detail", cause
			}
			return "", nil
		}, func() { cleaned++ })
		if !errors.Is(err, cause) || !strings.Contains(err.Error(), "failure detail") || calls != failAt+1 || cleaned != 1 {
			t.Fatalf("failure %d: %v, calls=%d cleanup=%d", failAt, err, calls, cleaned)
		}
	}
}

func TestTProxyGuardDetectsMissingRulesAndTUNRoute(t *testing.T) {
	snapshots := tproxyReadySnapshots(t)
	for _, missing := range []string{"", "VLESS_IN -p udp", "VLESS_TPROXY -p udp", "VLESS_OUTPUT -p tcp", "OUTPUT -j VLESS_OUTPUT", "INPUT -j VLESS_IN", "VLESS_TPROXY -m mark", "VLESS_TPROXY -d 10.0.0.0", "tun route", "late hook", "late mangle input"} {
		calls := 0
		command := func(name string, args ...string) (string, error) {
			calls++
			switch name + " " + strings.Join(args, " ") {
			case "ip rule show":
				return "1: from all fwmark 0x1 lookup 100\n2: from all fwmark 0x9911 lookup main", nil
			case "ip route show table 100":
				if missing == "tun route" {
					return "default dev tun0", nil
				}
				return "local default dev lo scope host", nil
			case "iptables -t mangle -S", "iptables -S":
				output := snapshots[1]
				if len(args) > 1 {
					output = snapshots[0]
				}
				var lines []string
				for _, line := range strings.Split(output, "\n") {
					if missing != "" && strings.Contains(line, missing) {
						continue
					}
					lines = append(lines, line)
				}
				output = strings.Join(lines, "\n")
				output = strings.ReplaceAll(output, "--set-mark", "--set-xmark")
				if missing == "late hook" {
					output = "-A OUTPUT -j _NDM_OUTPUT\n" + output
				}
				if missing == "late mangle input" && len(args) > 1 {
					output = "-A INPUT -p tcp --dport 443 -j _NDM_HTTP_INPUT_TLS_\n" + output
				}
				return output, nil
			default:
				t.Fatalf("unexpected guard command: %s %v", name, args)
				return "", nil
			}
		}
		if got := globalRouteReady(command); got != (missing == "") {
			t.Fatalf("guard missing=%q: %v", missing, got)
		}
		if calls > 4 {
			t.Fatalf("guard spawned %d processes", calls)
		}
	}
}

func TestTProxyProbeLoadsKeeneticModuleAndCleansUp(t *testing.T) {
	var calls []string
	loaded := false
	err := ensureTProxySupport(func(name string, args ...string) (string, error) {
		line := name + " " + strings.Join(args, " ")
		calls = append(calls, line)
		if name == "uname" {
			return "4.9-ndm-5\n", nil
		}
		if name == "insmod" {
			loaded = true
		}
		if strings.Contains(line, "-j TPROXY") && !loaded {
			return "missing module", errors.New("unavailable")
		}
		return "", nil
	})
	if err != nil || !loaded {
		t.Fatalf("load: %v, calls=%v", err, calls)
	}
	joined := strings.Join(calls, "\n")
	for _, required := range []string{"insmod /lib/modules/4.9-ndm-5/xt_TPROXY.ko", "VLESS_PROBE -p udp", "-X VLESS_PROBE"} {
		if !strings.Contains(joined, required) {
			t.Fatal("missing " + required)
		}
	}
	if strings.Contains(joined, "PREROUTING") || strings.Contains(joined, "OUTPUT") {
		t.Fatal("probe modified live hooks")
	}
}

func TestTProxyCleanupOnlyRemovesOwnedRules(t *testing.T) {
	var calls []string
	disableGlobalRoute(func(name string, args ...string) (string, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return "", errors.New("absent")
	})
	for _, line := range calls {
		if strings.Contains(line, "_NDM_") || line == "iptables -F" || strings.Contains(line, "del pref") {
			t.Fatal("unowned cleanup: " + line)
		}
	}
	joined := strings.Join(calls, "\n")
	for _, required := range []string{"-D OUTPUT -j VLESS_OUTPUT", "-i lo -j VLESS_TPROXY", "-D INPUT -j VLESS_IN", "-t mangle -D INPUT -j VLESS_IN", "-X VLESS_OUTPUT", "-t mangle -X VLESS_IN", "-X VLESS_IN", "-D OUTPUT -p tcp -j VLESS_TCP"} {
		if !strings.Contains(joined, required) {
			t.Fatal("missing cleanup " + required)
		}
	}
}
