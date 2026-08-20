// Command admin_panel_pack post-processes a directory of built SPA
// assets so it can be served straight from the Go binary via //go:embed.
// It does *not* generate any Go source any more — that responsibility
// moved to the embed directive in service/admin_panel/service.go.
//
// Three transformations happen here, all in-place inside the supplied
// directory:
//
//  1. Legacy WOFF (1.0) fallback fonts are deleted. Every browser made
//     after 2014 reads WOFF2 natively, so shipping both formats roughly
//     doubles the embedded font payload for no real-world benefit. The
//     matching `,url(*.woff) format("woff")` segments are stripped from
//     the bundled CSS in step (2) so the @font-face rules don't reference
//     files that aren't shipped.
//  2. Bundled CSS is rewritten to drop those WOFF URL fragments.
//  3. Compressible text assets (.html, .css, .js, .svg, .json, .map) are
//     pre-gzipped as companion `*.gz` files. The HTTP handler then either
//     passes those bytes through verbatim with Content-Encoding: gzip or
//     falls back to the raw file for the rare client that does not
//     advertise gzip support — no on-line compression, no surprises.
//     Already-compressed formats (.woff2, fonts, images) are skipped: gzip
//     can't shrink them and the duplicate would only inflate the binary.
package main

import (
	"bytes"
	"compress/gzip"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

func main() {
	dir := flag.String("dir", "service/admin_panel/dist", "directory of built SPA assets to post-process in place")
	flag.Parse()

	woffDropped, err := pruneWoff(*dir)
	if err != nil {
		fail("prune woff: %v", err)
	}
	cssRewritten, err := rewriteCSS(*dir)
	if err != nil {
		fail("rewrite css: %v", err)
	}
	stats, err := gzipText(*dir)
	if err != nil {
		fail("gzip text: %v", err)
	}
	fmt.Fprintf(os.Stderr, "post-processed %s: dropped %d woff, rewrote %d css, gzipped %d files (%d→%d bytes)\n",
		*dir, woffDropped, cssRewritten, stats.gzipped, stats.totalRaw, stats.totalGz)
}

// pruneWoff deletes every legacy *.woff (WOFF 1.0) font under dir. The
// bundled CSS still references them on entry; rewriteCSS drops those
// references in a separate pass so the two operations stay independently
// testable.
func pruneWoff(dir string) (int, error) {
	var n int
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || !strings.EqualFold(filepath.Ext(p), ".woff") {
			return nil
		}
		if err := os.Remove(p); err != nil {
			return err
		}
		n++
		return nil
	})
	return n, err
}

// woffRefAfterRE / woffRefBeforeRE match a single WOFF 1.0 entry inside a
// Vite-bundled CSS `src:` declaration. Vite minifies the rule to
// `src:url(./X.woff2) format("woff2"),url(./X.woff) format("woff");` so the
// "after" regex (the common case) eats the *leading* comma + woff entry,
// leaving only the woff2 source. We also handle the rare reverse ordering
// in a second pass.
var (
	woffRefAfterRE  = regexp.MustCompile(`,\s*url\([^)]*\.woff\)\s*format\(["']woff["']\)`)
	woffRefBeforeRE = regexp.MustCompile(`url\([^)]*\.woff\)\s*format\(["']woff["']\)\s*,\s*`)
)

// rewriteCSS drops every reference to a *.woff URL from every *.css file
// under dir. Pairs naturally with pruneWoff: after both passes, no font
// URL in the bundle points at a file that isn't shipped.
func rewriteCSS(dir string) (int, error) {
	var n int
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || !strings.EqualFold(filepath.Ext(p), ".css") {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out := woffRefAfterRE.ReplaceAll(data, nil)
		out = woffRefBeforeRE.ReplaceAll(out, nil)
		if bytes.Equal(out, data) {
			return nil
		}
		if err := os.WriteFile(p, out, 0o644); err != nil {
			return err
		}
		n++
		return nil
	})
	return n, err
}

// gzipExts is the set of file extensions for which a `.gz` companion is
// generated. Anything not on this list is left alone — woff2/png/jpeg/etc.
// are already compressed, so gzip can only inflate them slightly while
// doubling the embedded payload.
var gzipExts = map[string]bool{
	".html": true,
	".css":  true,
	".js":   true,
	".mjs":  true,
	".svg":  true,
	".json": true,
	".map":  true,
	".txt":  true,
	".xml":  true,
	".wasm": true,
}

type gzipStats struct {
	gzipped  int
	totalRaw int64
	totalGz  int64
}

// gzipText produces a `<file>.gz` companion next to every text-like asset
// in dir, using gzip.BestCompression. The companion is dropped if the
// compressed bytes don't save at least 10 % over the raw file — same
// heuristic we used in the previous (Go-source-emitting) generation, just
// applied to disk files now.
func gzipText(dir string) (gzipStats, error) {
	var stats gzipStats
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(p))
		if ext == ".gz" || !gzipExts[ext] {
			return nil
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		var buf bytes.Buffer
		w, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
		if err != nil {
			return err
		}
		// Reproducible: no mtime, no OS marker.
		w.ModTime = time.Time{}
		w.OS = 0xff
		if _, err := w.Write(raw); err != nil {
			return err
		}
		if err := w.Close(); err != nil {
			return err
		}
		if buf.Len() > len(raw)*9/10 {
			return nil
		}
		stats.gzipped++
		stats.totalRaw += int64(len(raw))
		stats.totalGz += int64(buf.Len())
		return os.WriteFile(p+".gz", buf.Bytes(), 0o644)
	})
	return stats, err
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "admin_panel_pack: "+format+"\n", args...)
	os.Exit(1)
}
