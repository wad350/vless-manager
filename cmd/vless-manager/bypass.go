package main

import (
	"context"
	_ "embed"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
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
// sing-box `direct` route rule. Order:
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

func fetchBypassWhitelist(client *http.Client) ([]string, error) {
	return fetchBypassWhitelistContext(context.Background(), client)
}

func fetchBypassWhitelistContext(ctx context.Context, client *http.Client) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, bypassWhitelistURL, nil)
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
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, fmt.Errorf("read bypass list: %w", err)
	}
	domains := parseDomainList(string(body))
	if len(domains) < 100 {
		return nil, fmt.Errorf("bypass list is unexpectedly small: %d domains", len(domains))
	}
	return domains, nil
}

func directBypassHTTPClient(timeout time.Duration) *http.Client {
	return subscriptionHTTPClient(timeout, nil)
}
