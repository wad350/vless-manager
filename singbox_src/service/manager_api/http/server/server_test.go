package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sagernet/sing-box/option"
)

func TestCORSMiddleware_Preflight(t *testing.T) {
	called := false
	h := newCORSMiddleware(nil)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}))

	req := httptest.NewRequest(http.MethodOptions, "/manager/v1/squads", nil)
	req.Header.Set("Origin", "http://localhost:8081")
	req.Header.Set("Access-Control-Request-Method", "GET")
	req.Header.Set("Access-Control-Request-Headers", "Authorization, Content-Type")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("preflight status = %d, want 204", rec.Code)
	}
	if called {
		t.Fatal("next handler should not run for OPTIONS preflight")
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:8081" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want echoed origin", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Headers"); got != "Authorization, Content-Type" {
		t.Fatalf("Access-Control-Allow-Headers = %q, want echoed request headers", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Methods"); got == "" {
		t.Fatal("Access-Control-Allow-Methods should be set")
	}
}

func TestCORSMiddleware_PassesThroughGET(t *testing.T) {
	called := false
	h := newCORSMiddleware(nil)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/manager/v1/squads/count", nil)
	req.Header.Set("Origin", "http://localhost:8081")
	req.Header.Set("Authorization", "Bearer test")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if !called {
		t.Fatal("next handler should run for GET")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:8081" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want echoed origin", got)
	}
	if got := rec.Header().Get("Vary"); got != "Origin" {
		t.Fatalf("Vary = %q, want Origin", got)
	}
}

func TestCORSMiddleware_NoOriginFallsBackToWildcard(t *testing.T) {
	h := newCORSMiddleware(nil)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	req := httptest.NewRequest(http.MethodGet, "/manager/v1/squads/count", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want * for missing origin", got)
	}
}

func TestCORSMiddleware_AllowedOriginsAllowList(t *testing.T) {
	cfg := &option.ManagerAPICORSOptions{
		AllowedOrigins: []string{"https://panel.example.com"},
	}
	h := newCORSMiddleware(cfg)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	allowed := httptest.NewRequest(http.MethodGet, "/manager/v1/squads/count", nil)
	allowed.Header.Set("Origin", "https://panel.example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, allowed)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://panel.example.com" {
		t.Fatalf("allowed origin = %q, want exact echo", got)
	}

	denied := httptest.NewRequest(http.MethodGet, "/manager/v1/squads/count", nil)
	denied.Header.Set("Origin", "https://attacker.example")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, denied)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("denied origin = %q, want empty", got)
	}
}

func TestCORSMiddleware_StaticCredentialsHeader(t *testing.T) {
	h := newCORSMiddleware(nil)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	req := httptest.NewRequest(http.MethodGet, "/manager/v1/squads/count", nil)
	req.Header.Set("Origin", "https://panel.example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "" {
		t.Fatalf("Access-Control-Allow-Credentials = %q, want empty (credentials are statically disabled)", got)
	}
}

func TestCORSMiddleware_FullConfig(t *testing.T) {
	cfg := &option.ManagerAPICORSOptions{
		AllowedOrigins: []string{"*"},
		ExposedHeaders: []string{"X-Total-Count"},
		MaxAge:         120,
	}
	h := newCORSMiddleware(cfg)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	req := httptest.NewRequest(http.MethodOptions, "/manager/v1/squads", nil)
	req.Header.Set("Origin", "https://panel.example.com")
	req.Header.Set("Access-Control-Request-Headers", "Authorization, Content-Type")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("preflight status = %d, want 204", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Methods"); got != "GET, POST, PUT, DELETE, OPTIONS" {
		t.Fatalf("Allow-Methods = %q, want static methods list", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Headers"); got != "Authorization, Content-Type" {
		t.Fatalf("Allow-Headers = %q, want echoed request headers", got)
	}
	if got := rec.Header().Get("Access-Control-Expose-Headers"); got != "X-Total-Count" {
		t.Fatalf("Expose-Headers = %q, want %q", got, "X-Total-Count")
	}
	if got := rec.Header().Get("Access-Control-Max-Age"); got != "120" {
		t.Fatalf("Max-Age = %q, want %q", got, "120")
	}
}
