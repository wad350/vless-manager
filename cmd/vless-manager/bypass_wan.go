package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"time"
)

func bypassWANHTTPClient(timeout time.Duration) *http.Client {
	dialer := wanDialer(timeout)
	// Source downloads and DNS must bypass the tunnel, including TCP DNS fallback.
	dnsDialer := wanDialer(timeout)
	dialer.Resolver = &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
		if network == "udp" {
			network = "udp4"
		} else if network == "tcp" {
			network = "tcp4"
		}
		return dnsDialer.DialContext(ctx, network, address)
	}}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DisableKeepAlives: true,
			DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
				host, port, err := net.SplitHostPort(address)
				if err != nil {
					return nil, err
				}
				ips, err := dialer.Resolver.LookupNetIP(ctx, "ip4", host)
				if err != nil {
					return nil, err
				}
				var lastErr error
				for _, ip := range ips {
					if !bypassPublicIPv4(ip) {
						continue
					}
					conn, err := dialer.DialContext(ctx, "tcp4", net.JoinHostPort(ip.String(), port))
					if err == nil {
						return conn, nil
					}
					lastErr = err
				}
				if lastErr != nil {
					return nil, lastErr
				}
				return nil, fmt.Errorf("no public IPv4 address for %s", host)
			},
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func bypassPublicIPv4(ip netip.Addr) bool {
	if !ip.Is4() || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	bytes := ip.As4()
	return bytes[0] != 0 && bytes[0] < 224 &&
		!(bytes[0] == 100 && bytes[1] >= 64 && bytes[1] < 128) &&
		!(bytes[0] == 198 && (bytes[1] == 18 || bytes[1] == 19))
}
