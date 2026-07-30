package main

import (
	"encoding/json"
	"os"
	"testing"
)

func TestFetchSubscriptionToFile(t *testing.T) {
	url := os.Getenv("VLESS_MANAGER_FETCH_SUB_URL")
	out := os.Getenv("VLESS_MANAGER_FETCH_SUB_OUT")
	if url == "" || out == "" {
		t.Skip("set VLESS_MANAGER_FETCH_SUB_URL and VLESS_MANAGER_FETCH_SUB_OUT")
	}
	sub, err := fetchSubscription(url)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent([]*Subscription{sub}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, data, 0644); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %d servers to %s", len(sub.Servers), out)
}
