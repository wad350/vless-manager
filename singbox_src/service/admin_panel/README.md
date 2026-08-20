# admin_panel

A TypeScript (Vite + React + Material UI, dark theme) admin panel that talks to
the `manager_api/http` server.

The compiled SPA is **embedded into the Go binary via `//go:embed`** — the
post-processed `dist/` directory is checked into the repo, so
`go build -tags with_admin_panel ./cmd/sing-box` produces a fully
self-contained binary with no Node.js required at compile time.

The `web/` directory holds the original TypeScript sources, which are only
needed when the panel itself is being modified. `make admin_panel_regen`
rebuilds `dist/` from those sources via `npm run build` followed by an
in-place post-processing step (`cmd/internal/admin_panel_pack`).

## Layout

```
service/admin_panel/
├── service.go         # Go service (build tag: with_admin_panel, //go:embed dist)
├── service_stub.go    # Stub when the tag is missing
├── service_test.go    # Tests for the SPA handler
├── dist/              # Embedded SPA bytes (committed to git)
│   ├── index.html
│   ├── index.html.gz
│   └── assets/
│       ├── index-*.js
│       ├── index-*.js.gz
│       ├── index-*.css
│       ├── index-*.css.gz
│       └── inter-*.woff2
└── web/               # Vite + React TypeScript source (only needed to regen)
    ├── package.json
    ├── vite.config.ts
    ├── tsconfig.json
    ├── index.html
    └── src/
        ├── main.tsx
        ├── App.tsx
        ├── theme.ts
        ├── api/{client,types}.ts
        ├── auth/AuthContext.tsx
        ├── components/{Layout,CrudPage}.tsx
        └── pages/*.tsx

cmd/internal/admin_panel_pack/
└── main.go            # Post-processor: drops .woff, rewrites CSS, pre-gzips text
```

## Building sing-box with the panel

`dist/` is committed, so any developer or CI machine can build a
self-contained binary with no Node.js / npm available:

```bash
go build -tags with_admin_panel ./cmd/sing-box
# or
make build_admin_panel
```

If the tag is omitted the service registers a stub that errors out on start.

## Regenerating dist/

Whenever the SPA source under `web/` is changed, run:

```bash
make admin_panel_regen
```

That target performs two steps:

1. `make admin_panel_web` — `npm install` + `npm run build` in `web/`,
   producing `service/admin_panel/dist/`.
2. `make admin_panel_pack` — runs the
   `cmd/internal/admin_panel_pack` post-processor, which:
   - deletes the legacy `*.woff` (WOFF 1.0) fallback fonts; every browser
     since 2014 reads WOFF2 natively, and shipping both formats roughly
     doubles the embedded font payload;
   - rewrites the bundled `*.css` to drop `,url(...).woff) format("woff")`
     references so the @font-face rules don't point at files that aren't
     shipped;
   - drops a gzip-compressed `*.gz` companion next to every text-like
     asset (.html, .css, .js, .svg, .json) at `gzip.BestCompression`. The
     runtime serves those bytes verbatim with `Content-Encoding: gzip`
     when the client advertises gzip support, and falls back to the raw
     file otherwise.

After the post-processing pass, commit the resulting `dist/` directory
along with your source changes.

## Configuring sing-box

Add a service entry of type `admin-panel`:

```json
{
  "services": [
    {
      "type": "admin-panel",
      "tag": "admin",
      "listen": "127.0.0.1",
      "listen_port": 8081
    }
  ]
}
```

The panel itself does not require any back-end auth: at sign-in the user
enters the URL and bearer key of a `manager-api` instance. Both are stored
only in the browser's `localStorage`.
