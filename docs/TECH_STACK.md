# rua: Tech Stack

Status — draft

rua follows `basket` and `family-tab`: a Go server with an embedded React UI,
released with goreleaser and the shared `release-workflows`.

| Area | Choice |
| --- | --- |
| Server | Go, standard library `net/http` and `ServeMux`, single static binary with the built UI embedded |
| Shared code | `go-commons` for logging, panic recovery, health and metrics middleware |
| IMAP | `github.com/emersion/go-imap/v2` client, wrapped in a read-only interface; tests use its `imapserver` and `imapmemserver` |
| MIME | `github.com/emersion/go-message` |
| Storage | SQLite via `modernc.org/sqlite` (no cgo), WAL, `PRAGMA user_version` migrations, one writer connection |
| Compression | `klauspost/compress/zstd` for stored blobs; stdlib `compress/gzip` and `archive/zip` for reports |
| Enrichment | stdlib `net.Resolver` for PTR and Team Cymru DNS; `golang.org/x/net/publicsuffix` |
| Config | Environment variables via `sethvargo/go-envconfig`, `_FILE` variants for secrets |
| Client | React, Vite, TypeScript, Tailwind CSS, TanStack Query; charts are small hand-written SVG components |
| Tests | `go test -race`, Go fuzzing, `node --test`, Playwright against `rua demo` |
| Release | goreleaser, `ghcr.io/zekihan/distroless`, images on GHCR and Docker Hub, binaries on GitHub releases |
| License | AGPL-3.0-only |

No chart library: the views need stacked bars, sparklines and a small donut,
which are a few dozen lines of SVG each, theme with CSS variables and keep the
bundle small. No client-side router library beyond the URL search params the
views already read.

## Layout

| Path | Contents |
| --- | --- |
| `cmd/rua` | Entry point and subcommands (`serve`, `fetch`, `import`, `reparse`, `demo`) |
| `internal/config` | Environment configuration |
| `internal/imapsrc` | Read-only IMAP client, incremental fetch, backoff |
| `internal/ingest` | Classification, extraction limits, deduplication, commit per message |
| `internal/parse/dmarc` | Aggregate (RFC 7489, DMARCbis) and failure report parsing |
| `internal/parse/tlsrpt` | RFC 8460 parsing |
| `internal/store` | Schema, migrations, blobs, reports, reparse |
| `internal/enrich` | PTR, ASN, cache, rate limit |
| `internal/sources` | Source rules and grouping |
| `internal/query` | Aggregations behind `/api` |
| `internal/server` | Routing, `/api` handlers, security headers, SPA serving |
| `internal/demo` | Synthetic dataset generator |
| `web/src` | React app |
| `testdata/` | Report fixtures |
| `e2e/` | Playwright tests |

## Image

Built like `basket`: goreleaser produces `linux/amd64` and `linux/arm64`
binaries, the Dockerfile copies one onto `ghcr.io/zekihan/distroless`, runs as a
fixed non-root UID with `/data` as the only writable path besides `/tmp`, and
sets `DB_PATH=/data/rua.db`.
