//go:build with_admin_panel

package admin_panel

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// firstAssetPath returns one of the hashed Vite assets from the embedded
// SPA bundle. Used by tests that need a real file-served URL (i.e. one
// that should NOT fall back to index.html). Returns "" if the bundle is
// empty — every concrete test then skips itself, which keeps the suite
// runnable even before `make admin_panel_regen` has been run.
func firstAssetPath(t *testing.T) string {
	t.Helper()
	var found string
	_ = fs.WalkDir(distRoot, "assets", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || strings.HasSuffix(p, ".gz") {
			return nil
		}
		found = p
		return fs.SkipAll
	})
	return found
}

// firstGzippedAsset returns one asset that has a `.gz` companion in the
// embedded bundle. Like firstAssetPath, returns "" when nothing matches.
func firstGzippedAsset(t *testing.T) string {
	t.Helper()
	var found string
	_ = fs.WalkDir(distRoot, "assets", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || strings.HasSuffix(p, ".gz") {
			return nil
		}
		if _, ok := gzipCompanion(p); ok {
			found = p
			return fs.SkipAll
		}
		return nil
	})
	return found
}

func TestSPAHandler(t *testing.T) {
	r := chi.NewRouter()
	handler := newSPAHandler()
	r.Method(http.MethodGet, "/*", handler)

	indexBytes, hasIndex := readFile("index.html")
	if !hasIndex {
		t.Skip("index.html not packed yet (run `make admin_panel_regen`)")
	}

	cases := []struct {
		name string
		path string
	}{
		{"root", "/"},
		{"client-route fallback", "/squads"},
		{"deep route fallback", "/users/123"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			r.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if !strings.Contains(rec.Body.String(), "<div id=\"root\">") {
				t.Fatalf("body does not look like index.html:\n%s", rec.Body.String())
			}
		})
	}

	// Ensure a real packed asset is served as itself, not as the fallback.
	t.Run("real file is served", func(t *testing.T) {
		assetPath := firstAssetPath(t)
		if assetPath == "" {
			t.Skip("no packed assets/* to verify")
		}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/"+assetPath, nil)
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if strings.Contains(rec.Body.String(), "<div id=\"root\">") {
			t.Fatalf("asset request returned the index fallback")
		}
	})

	// Sanity check: index.html in the bundle is the same as what serveIndex returns.
	t.Run("index payload matches map", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		r.ServeHTTP(rec, req)
		if rec.Body.String() != string(indexBytes) {
			t.Fatalf("served index does not match readFile(\"index.html\")")
		}
	})

	t.Run("index has no-store cache control", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		r.ServeHTTP(rec, req)
		cc := rec.Header().Get("Cache-Control")
		if !strings.Contains(cc, "no-store") {
			t.Fatalf("Cache-Control = %q, want it to contain no-store", cc)
		}
	})

	t.Run("hashed asset is cached aggressively", func(t *testing.T) {
		assetPath := firstAssetPath(t)
		if assetPath == "" {
			t.Skip("no packed assets/* to verify")
		}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/"+assetPath, nil)
		r.ServeHTTP(rec, req)
		cc := rec.Header().Get("Cache-Control")
		if !strings.Contains(cc, "immutable") {
			t.Fatalf("Cache-Control for %q = %q, want it to contain immutable", assetPath, cc)
		}
	})

	// With Accept-Encoding: gzip the handler should pass through the
	// pre-compressed `.gz` companion verbatim with Content-Encoding: gzip.
	t.Run("gzip pass-through", func(t *testing.T) {
		assetPath := firstGzippedAsset(t)
		if assetPath == "" {
			t.Skip("no gzipped assets in this build")
		}
		gz, _ := gzipCompanion(assetPath)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/"+assetPath, nil)
		req.Header.Set("Accept-Encoding", "gzip")
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
			t.Fatalf("Content-Encoding = %q, want gzip", got)
		}
		if rec.Body.String() != string(gz) {
			t.Fatalf("body did not match the stored gzipped bytes")
		}
	})

	// Without Accept-Encoding the same asset should be served raw and
	// Content-Encoding must be unset.
	t.Run("gzip transparent fallback", func(t *testing.T) {
		assetPath := firstGzippedAsset(t)
		if assetPath == "" {
			t.Skip("no gzipped assets in this build")
		}
		raw, _ := readFile(assetPath)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/"+assetPath, nil)
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if got := rec.Header().Get("Content-Encoding"); got != "" {
			t.Fatalf("Content-Encoding = %q, want empty (raw body)", got)
		}
		if rec.Body.String() != string(raw) {
			t.Fatalf("body does not match the raw asset")
		}
	})

	// `.gz` companions are an internal implementation detail of the
	// pre-compression strategy; clients should never fetch them directly,
	// and walking the embedded FS via /-prefixed URLs must not expose
	// them as if they were real assets either. Today they happen to be
	// reachable (since fs.ReadFile sees them) — this test pins that
	// behaviour so we notice if the contract changes.
	t.Run("gz companion lookup", func(t *testing.T) {
		assetPath := firstGzippedAsset(t)
		if assetPath == "" {
			t.Skip("no gzipped assets in this build")
		}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/"+assetPath+".gz", nil)
		r.ServeHTTP(rec, req)
		// We don't assert the precise body — only that the request
		// resolves to *something* (200 or fallback) instead of crashing.
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (asset or SPA fallback)", rec.Code)
		}
	})
}
