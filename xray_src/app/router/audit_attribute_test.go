package router_test

import (
	"strings"
	"testing"

	"github.com/xtls/xray-core/app/router"
	"github.com/xtls/xray-core/common/session"
	routing_session "github.com/xtls/xray-core/features/routing/session"
)

func TestRoutingAttributesRejectInvalidPatterns(t *testing.T) {
	for _, pattern := range []string{"[", "(", "*"} {
		t.Run(pattern, func(t *testing.T) {
			defer func() {
				if recovered := recover(); recovered != nil {
					t.Fatalf("invalid attribute pattern panicked: %v", recovered)
				}
			}()
			_, err := (&router.RoutingRule{Attributes: map[string]string{"X-Test": pattern}}).BuildCondition()
			if err == nil || !strings.Contains(err.Error(), "X-Test") {
				t.Fatalf("invalid pattern error = %v, want contextual attribute error", err)
			}
		})
	}
}

func TestRoutingAttributesPreserveValidMatching(t *testing.T) {
	condition, err := (&router.RoutingRule{Attributes: map[string]string{
		":Path":  "^/api(?:/.*)?$",
		"Custom": "p([a-z]+)ch",
	}}).BuildCondition()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name       string
		attributes map[string]string
		want       bool
	}{
		{"matching", map[string]string{":path": "/api/items", "CUSTOM": "peach"}, true},
		{"root", map[string]string{":PATH": "/api", "custom": "peach"}, true},
		{"anchored", map[string]string{":path": "/outside/api", "custom": "peach"}, false},
		{"case_sensitive_value", map[string]string{":path": "/api", "custom": "PEACH"}, false},
		{"missing_key", map[string]string{":path": "/api"}, false},
		{"nil", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := &routing_session.Context{Content: &session.Content{Attributes: tc.attributes}}
			if got := condition.Apply(ctx); got != tc.want {
				t.Fatalf("attribute match = %v, want %v", got, tc.want)
			}
		})
	}
}
