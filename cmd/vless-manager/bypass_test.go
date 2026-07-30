package main

import (
	"testing"
	"time"
)

func TestBypassDomainsUsesUpdatedCacheAndUserEntries(t *testing.T) {
	cfg := defaultConfig()
	cfg.BypassCache = BypassCache{
		Domains:   []string{"cached.example", "DUP.example"},
		UpdatedAt: time.Now(),
		Source:    bypassWhitelistURL,
	}
	cfg.Settings.BypassDomains = []string{"user.example", "dup.example"}

	got := bypassDomainsFor(cfg)
	want := []string{"cached.example", "dup.example", "user.example"}
	if len(got) != len(want) {
		t.Fatalf("got %d domains %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("domain[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestBypassDomainsFallsBackToEmbeddedList(t *testing.T) {
	cfg := defaultConfig()
	got := bypassDomainsFor(cfg)
	if len(got) < 100 {
		t.Fatalf("embedded bypass list unexpectedly small: %d", len(got))
	}
}
