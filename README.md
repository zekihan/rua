# rua

A self-hosted viewer for DMARC aggregate reports and SMTP TLS reports. It reads
the mailbox your `rua` address delivers to, fetches each new message once
without changing anything in the mailbox, and serves a web UI that answers who
sends as your domains, what fails and why.

- Incremental, read-only IMAP (`EXAMINE` and `BODY.PEEK` only).
- DMARC aggregate (RFC 7489 and DMARCbis), TLS-RPT (RFC 8460); failure reports
  are listed by header.
- Senders grouped by organisation (ASN, PTR) or your own rules; forwarded mail
  told apart from spoofing.
- Raw reports kept, so parser upgrades reprocess locally.
- One binary, one SQLite file, Prometheus metrics.

rua has no authentication. Run it behind something that authenticates, such as
a reverse proxy with mTLS or an identity-aware proxy.

See [docs/DESIGN.md](docs/DESIGN.md) for the design and
[docs/TECH_STACK.md](docs/TECH_STACK.md) for the stack.

## Try it

```sh
make web
go run ./cmd/rua demo    # a generated year of reports, served from memory on :8080
```

## Run

```sh
docker run -p 8080:8080 -v rua:/data \
  -e IMAP_ADDR=imap.example.com:993 \
  -e IMAP_USERNAME=dmarc@example.com \
  -e IMAP_PASSWORD_FILE=/run/secrets/imap-password \
  -e IMAP_MAILBOXES=INBOX \
  ghcr.io/zekihan/rua serve
```

The first fetch backfills the whole mailbox; later fetches only read new
messages. Without IMAP, load files instead:

```sh
rua import reports/ archive.mbox    # .xml, .xml.gz, .zip, .json, .json.gz, .eml, mbox
```

| Command | Purpose |
| --- | --- |
| `rua serve` | Server, fetch loop and enrichment (default) |
| `rua fetch` | One fetch and exit |
| `rua import <path>...` | Local files and directories |
| `rua reparse` | Reparse all stored reports now |
| `rua demo` | Serve generated data from memory |

## Configuration

Environment variables; any of them may instead be read from a file named by
`<NAME>_FILE`.

| Variable | Default | Purpose |
| --- | --- | --- |
| `IMAP_ADDR` | empty | `host:port`; IMAP disabled when empty |
| `IMAP_TLS` | `tls` | `tls`, `starttls`, or `none` (loopback only) |
| `IMAP_CA_FILE` | empty | Extra trust root for the IMAP connection |
| `IMAP_USERNAME`, `IMAP_PASSWORD` | required with `IMAP_ADDR` | |
| `IMAP_MAILBOXES` | `INBOX` | Comma-separated; a missing mailbox is an error |
| `FETCH_INTERVAL` | `30m` | ±10% jitter; backs off up to 6h while failing |
| `STALE_AFTER` | `3h` | `/api/status` reports `stale: true` after this |
| `MAX_MESSAGE_BYTES` | `26214400` | Larger messages are recorded but not fetched |
| `DB_PATH` | `./rua.db` | SQLite database (`/data/rua.db` in the image) |
| `RETENTION_DAYS` | `0` | Delete reports older than this; 0 keeps everything |
| `SOURCES_FILE` | empty | Source rules, below |
| `LOOKUPS` | `on` | PTR and ASN lookups (Team Cymru DNS); `off` disables |
| `LISTEN_ADDR` | `:8080` | HTTP listener |
| `METRICS_ADDR` | `:9090` | Internal `/metrics`; empty disables |
| `LOG_LEVEL` | `INFO` | |

## Source rules

Senders are grouped by AS organisation, then by the domain of a
forward-confirmed PTR, then by IP. Rules name your known senders and mark them
as expected; every other sender is shown as unknown. The first matching rule
wins, and the file is reloaded when it changes.

```yaml
- name: Mail provider
  expected: true
  match:
    dkim_domains: [example.com]
    asns: [64500]
- name: Newsletter service
  expected: true
  match:
    cidrs: [192.0.2.0/24]
    ptr_suffixes: [mailer.example.net]
```

## Monitoring

`GET /api/status` includes `stale`, suitable for an uptime check, and
`GET /healthz` reports liveness. Metrics on `METRICS_ADDR` include
`rua_last_successful_fetch_timestamp_seconds`, `rua_ingest_stale`,
`rua_fetch_runs_total`, `rua_messages`, `rua_records_messages` and
`rua_enrich_queue_length`.

## Development

```sh
make web        # build the frontend
make dev        # build the frontend and run the server
make test       # Go and web tests
make lint       # golangci-lint
make test/e2e   # Playwright against rua demo
```

## License

AGPL-3.0-only.
