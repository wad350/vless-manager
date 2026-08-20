import { defineConfig, type Plugin } from "vite";
import react from "@vitejs/plugin-react";

// preloadInterFonts injects `<link rel="preload">` tags into the
// production index.html for the latin subset of every Inter weight
// the SPA imports (400/500/600/700). The font files come from
// `@fontsource/inter` and are hashed by Vite into `dist/assets/`,
// so the filenames aren't known until after the bundle is emitted —
// hence a `transformIndexHtml` hook scoped to build mode that reads
// `ctx.bundle` and matches the emitted woff2 names.
//
// Why latin only: the admin panel UI is English-only ASCII, so the
// latin subset is what the very first paint actually needs. Preload-
// ing every fontsource subset (latin, latin-ext, cyrillic, cyrillic-
// ext, greek, greek-ext × 4 weights = 24 files) would just waste
// bandwidth and starve more critical resources of priority. If the
// UI ever needs to render non-ASCII glyphs at first paint, this list
// can be extended.
function preloadInterFonts(): Plugin {
  const WEIGHTS = ["400", "500", "600", "700"] as const;
  return {
    name: "preload-inter-fonts",
    apply: "build",
    transformIndexHtml: {
      order: "post",
      handler(html, ctx) {
        if (!ctx.bundle) return html;
        const tags: string[] = [];
        for (const weight of WEIGHTS) {
          const re = new RegExp(`inter-latin-${weight}-normal-[^/]+\\.woff2$`);
          const fileName = Object.keys(ctx.bundle).find((n) => re.test(n));
          if (!fileName) continue;
          // `base: "/"` is used in this config (so deep BrowserRouter
          // URLs like `/users` resolve `assets/...` from the site root
          // rather than relative to the current path). Emit matching
          // `/` prefixes here so the preload hits the exact same URL
          // the browser would otherwise request from the fontsource
          // @font-face rule, deduplicating the fetch instead of
          // doubling it.
          tags.push(
            `<link rel="preload" href="/${fileName}" as="font" type="font/woff2" crossorigin>`,
          );
        }
        if (tags.length === 0) return html;
        return html.replace("</head>", `    ${tags.join("\n    ")}\n  </head>`);
      },
    },
  };
}

export default defineConfig({
  plugins: [react(), preloadInterFonts()],
  // Absolute base so the SPA's asset URLs resolve from the site root.
  // The Go backend serves index.html for every unknown path (BrowserRouter
  // deep-link fallback); with a relative base, navigating directly to e.g.
  // `/users` would make the embedded `./assets/index-XYZ.js` URL resolve
  // to `/assets/index-XYZ.js` only when sitting at the root — at any
  // multi-segment path it would resolve relative to that path and miss.
  base: "/",
  build: {
    target: "es2020",
    chunkSizeWarningLimit: 1500,
  },
});
