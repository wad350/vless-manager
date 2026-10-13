package main

import (
	"bufio"
	"context"
	_ "embed"
	"fmt"
	"io"
	"net"
	"net/http"
	"slices"
	"strings"
	"time"

	"golang.org/x/net/idna"
)

const bypassWhitelistURL = "https://raw.githubusercontent.com/hxehex/russia-mobile-internet-whitelist/main/whitelist.txt"

// bypassRussiaWhitelist is the curated list of RU operators' zero-rated /
// likely-whitelisted hosts from
// https://github.com/hxehex/russia-mobile-internet-whitelist
//
// Embedded at build time so the router needs no internet to load it. The file
// itself is plain text — one host per line, # is a comment.
//
//go:embed bypass_ru.txt
var bypassRussiaWhitelist string

// parseDomainList splits a newline list into trimmed entries, dropping
// blanks and comments. Used both for the embedded whitelist and for the
// user-supplied BypassDomains slice (when it carries multi-line input from
// the textarea).
func parseDomainList(raw string) []string {
	if raw == "" {
		return nil
	}
	lines := strings.Split(raw, "\n")
	out := make([]string, 0, len(lines))
	seen := make(map[string]bool, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.ToLower(line)
		if seen[line] {
			continue
		}
		seen[line] = true
		out = append(out, line)
	}
	return out
}

// bypassDomainsFor returns the combined domain_suffix list to feed into the
// Xray `direct` route rule. Order:
//
//  1. Built-in RU whitelist (if cfg toggle is on) — covers ya.ru, mail.ru,
//     vk.ru, gosuslugi, banks, Yandex/VK CDNs, etc. ~900 hosts.
//  2. User-supplied domains from AppSettings.BypassDomains — appended last
//     so duplicates from the built-in list are deduped.
//
// Empty result means no bypass rule is emitted (caller should skip the rule).
func bypassDomainsFor(cfg *Config) []string {
	seen := make(map[string]bool)
	var out []string
	add := func(d string) {
		d = strings.TrimSpace(strings.ToLower(d))
		if d == "" || strings.HasPrefix(d, "#") || seen[d] {
			return
		}
		seen[d] = true
		out = append(out, d)
	}
	if cfg.Settings.BypassRouteRussia {
		base := bypassRussiaWhitelist
		if len(cfg.BypassCache.Domains) > 0 {
			base = strings.Join(cfg.BypassCache.Domains, "\n")
		}
		for _, d := range parseDomainList(base) {
			add(d)
		}
	}
	for _, d := range cfg.Settings.BypassDomains {
		add(d)
	}
	return out
}

func fetchBypassDomainSource(ctx context.Context, client *http.Client, source string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "vless-manager/1")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch bypass list: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("fetch bypass list: HTTP %d", resp.StatusCode)
	}
	const maxSize = 2 << 20
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxSize+1))
	if err != nil {
		return nil, fmt.Errorf("read bypass list: %w", err)
	}
	if len(body) > maxSize {
		return nil, fmt.Errorf("bypass list exceeds %d bytes", maxSize)
	}
	var domains []string
	seen := make(map[string]bool)
	scanner := bufio.NewScanner(strings.NewReader(string(body)))
	for scanner.Scan() {
		domain := strings.ToLower(strings.TrimSpace(scanner.Text()))
		if domain == "" || strings.HasPrefix(domain, "#") {
			continue
		}
		normalized, err := idna.Lookup.ToASCII(domain)
		if err != nil {
			return nil, fmt.Errorf("invalid bypass source domain %q: %w", domain, err)
		}
		domain = strings.ToLower(normalized)
		if !validBypassSourceDomain(domain) {
			return nil, fmt.Errorf("invalid bypass source domain %q", domain)
		}
		if !seen[domain] {
			seen[domain] = true
			domains = append(domains, domain)
			if len(domains) > 10000 {
				return nil, fmt.Errorf("bypass list exceeds 10000 domains")
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("parse bypass source: %w", err)
	}
	if len(domains) < 100 {
		return nil, fmt.Errorf("bypass list has unexpected size: %d domains", len(domains))
	}
	slices.Sort(domains)
	return domains, nil
}

func directBypassHTTPClient(timeout time.Duration) *http.Client {
	return bypassWANHTTPClient(timeout)
}

// Source files cannot grant whole TLDs, IP ranges or Xray rule expressions.
func validBypassSourceDomain(domain string) bool {
	if len(domain) > 253 || !strings.Contains(domain, ".") || net.ParseIP(domain) != nil || strings.HasSuffix(domain, ".arpa") {
		return false
	}
	for _, label := range strings.Split(domain, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

func bypassEffectiveCount(cfg *Config) int {
	return len(bypassListDomainsFor(cfg))
}

func bypassListDomainsFor(cfg *Config) []string {
	domains := bypassDomainsFor(cfg)
	slices.Sort(domains)
	return slices.Compact(domains)
}
