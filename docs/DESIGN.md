# rua: Design Doc

Status — draft

## 1. Problem

Domains with a DMARC record ask receivers to mail aggregate reports to a `rua`
address. Those reports are compressed XML attachments, one per receiver per day
per domain, and are unreadable as they arrive. The same mailbox also receives
SMTP TLS reports (RFC 8460) because the TLS-RPT record points at the same
address.

The homelab currently runs `tierpod/dmarc-report-converter` as an hourly
CronJob that downloads the whole folder, rewrites static HTML onto an SMB share
and serves it with nginx. It is unmaintained upstream, needs a carried patch for
IMAP connect retries, marks mail read, rebuilds everything every run, merges
reports in ways that lose detail, ignores TLS reports and answers no question
beyond "here is one report as a table".

rua replaces it. It is written from the questions an owner actually asks, not
from the old tool's options.

## 2. Questions it answers

| Question | Where |
| --- | --- |
| Is any of my mail failing DMARC, and how much? | Overview: pass rate and failing volume per domain |
| Who sends as my domain? | Sources: senders grouped by organisation, with volume and alignment |
| Is a new sender a misconfigured service or a spoofer? | Source detail: SPF/DKIM results, DKIM domains, PTR, ASN, verdict |
| Did a DNS change break something? | Overview chart, with published-policy changes marked on the timeline |
| Which receivers report, and are they still reporting? | Reporters: last report per receiver |
| Is TLS delivery to my MX failing? | TLS: session success and failure types per policy |
| Is the ingester healthy? | Status: last fetch, backlog, messages it could not use |

## 3. Goals and non-goals

Goals:

- Incremental, read-only IMAP ingestion: each message is fetched once, the
  mailbox is never modified.
- DMARC aggregate reports (RFC 7489 and the DMARCbis `dmarc-2.0` schema) and
  SMTP TLS reports (RFC 8460).
- A fast, readable web UI with shareable URLs, served by the same binary.
- Rebuildable state: raw reports are kept, so a parser change reprocesses
  locally without touching IMAP.
- One container, one SQLite file, small enough for a phone-sized node.
- Useful to anyone: public repository, no homelab assumptions in the binary.

Non-goals:

- Authentication or users. rua is deployed behind something that
  authenticates (mTLS in the homelab). The README says so plainly.
- Failure (`ruf`) report analysis. They are recognised, counted and listed with
  their headers only; no body or sample message is stored.
- Alerting. rua exposes metrics and a status endpoint; Gatus/Prometheus alert.
- Sending mail, DNS management, record generation or recommendations engines.
- Multi-tenancy, per-customer views, or report forwarding.
- Geolocation. Organisation (ASN) and PTR answer "who" better than a country.

## 4. Ingestion

### Mailbox access

- One IMAP account, one or more mailboxes (`IMAP_MAILBOXES`). A missing mailbox
  is an error; there is no fallback to `INBOX`.
- Mailboxes are opened with `EXAMINE`, never `SELECT`. Bodies are fetched with
  `BODY.PEEK[]`. The IMAP client is wrapped in an interface that exposes only
  `Examine`, `UIDSearch`, `UIDFetch` and `Logout`; a test asserts no
  state-changing command can be issued. Flags, folders and messages are never
  touched.
- TLS modes `tls`, `starttls` and `none` (`none` refuses to start unless the
  server is on loopback, for tests). `IMAP_CA_FILE` adds a trust root for this
  connection only; the system pool still applies otherwise.

### Incremental fetch

Per mailbox, rua stores `uidvalidity` and `next_uid` (the high-water mark).

1. `EXAMINE` the mailbox. If `UIDVALIDITY` differs from the stored value, record
   the change on the Status page, reset `next_uid` to 1 and continue. Already
   stored messages are recognised by content (below), so a reset costs one
   rescan and creates no duplicates.
2. `UID SEARCH UID <next_uid>:*`.
3. Fetch the result in batches of 50 UIDs (`UID`, `INTERNALDATE`,
   `RFC822.SIZE`, `BODY.PEEK[]`). Messages larger than `MAX_MESSAGE_BYTES`
   (default 25 MiB) are fetched as headers only and recorded as skipped.
4. Each message is committed in its own transaction: message row, raw blobs,
   parsed reports. `next_uid` advances only past a contiguous run of committed
   UIDs, so a crash mid-batch resumes at the first uncommitted message.
5. A message that cannot be parsed is still committed, with its classification
   and error, so it is not refetched. It is retried locally on the next parser
   version (section 6).

The first run backfills the whole mailbox the same way; there is no separate
import mode for IMAP.

### Schedule and backoff

- One fetch loop in the server process. Default interval 30 minutes with ±10%
  jitter. Fetches never overlap.
- Connect failures retry at 1, 2, 4, 8 seconds within one attempt (new pods are
  briefly refused while network policy catches up), then the attempt fails and
  the next one waits for the next interval, doubling up to 6 hours while
  failures continue. A Bridge-style server is never hammered with logins.
- The UI has a "Fetch now" button. It is accepted at most once per 5 minutes and
  only when no fetch is running.
- Reports are typically emitted once a day per receiver, so the default is not
  latency-sensitive; IMAP IDLE is not used.

### Local import

`rua import <path>...` ingests `.xml`, `.xml.gz`, `.zip`, `.json`, `.json.gz`,
`.eml` and `mbox` files through the same pipeline, for migrations and for people
without IMAP. Imported messages have no mailbox and are deduplicated the same
way.

## 5. Parsing

### Classification

Every message is classified once:

| Class | Detected by |
| --- | --- |
| `dmarc-aggregate` | Attachment or body is XML (optionally gzip/zip) with a `feedback` root |
| `tls-report` | `application/tlsrpt+gzip` / `+json`, or JSON with `policies` and `organization-name` |
| `dmarc-failure` | `multipart/report; report-type=feedback-report` with `Feedback-Type: auth-failure` |
| `other` | Anything else; listed on the Status page with subject and sender |

A message may carry several reports (several attachments); each is stored.

### Safety limits

- Decompression is streamed with a hard cap of 50 MiB per report and 200 MiB per
  message; zip archives with more than 20 entries or nested archives are
  rejected.
- XML is parsed with `encoding/xml` (no DTDs or external entities). Unknown
  elements are ignored; missing required elements fail that report only.
- Every string from a report is treated as untrusted: length-capped, stored as
  text and rendered escaped. Nothing from a report reaches a shell, a URL
  without encoding, or a DNS query other than PTR and ASN lookups of the record
  source IPs.

### Identity and deduplication

- A message is identified by the SHA-256 of its raw bytes; a report by
  `(kind, reporter org, report_id, policy domain)`.
- Receivers resend the same report (retries, multiple `rua` addresses). The
  second copy is stored as a duplicate link to the first, never counted twice.
- Reports with the same identity but different content keep the first and flag
  the conflict on the Status page.

Merging is never done at ingest. Every aggregation is a query over individual
records, so nothing is lost and any grouping can be added later.

## 6. Data model

SQLite, WAL, one writer connection. Times are UTC unix seconds.

| Table | Contents |
| --- | --- |
| `mailboxes` | name, uidvalidity, next_uid, last fetch result |
| `messages` | id, sha256, mailbox, uid, internaldate, size, from, subject, class, parse status, parser version |
| `blobs` | sha256, zstd-compressed raw bytes (whole message and each extracted report) |
| `reports` | id, kind, message, reporter org/email/extra contact, report_id, begin, end, policy domain, duplicate_of, conflict flag |
| `policies` | report, published policy: `p`, `sp`, `np`, `pct`, `adkim`, `aspf`, `fo`, testing |
| `records` | report, source IP, count, disposition, evaluated DKIM/SPF, override reasons, header_from, envelope_from, envelope_to |
| `auth_results` | record, method (dkim/spf), domain, selector, scope, result |
| `tls_policies` | report, policy type and string, MX hosts, success and failure totals |
| `tls_failures` | tls policy, result type, sending/receiving IP, MX hostname, failed session count, reason code |
| `ip_info` | IP, PTR, forward-confirmed flag, ASN, AS org, looked-up-at |
| `failure_reports` | message, arrival date, reported domain, source IP, auth failure type, original headers' From/To/Subject |
| `meta` | schema version, parser version |

Schema migrations use `PRAGMA user_version`. A bump of the compiled parser
version reparses every stored blob in the background and replaces derived rows,
so improving the parser never needs the mailbox again.

Retention: kept forever by default. `RETENTION_DAYS` deletes reports whose end
date is older, together with their records and, if unreferenced, blobs. The
mailbox remains the long-term archive.

## 7. Derived values

### DMARC verdict per record

The receiver's `policy_evaluated` is authoritative for pass/fail:

| Verdict | Rule |
| --- | --- |
| `pass` | evaluated DKIM or SPF is `pass` |
| `forwarded` | not `pass`, but a raw DKIM result for the header-from domain (or a parent, in relaxed mode) passed, or an override reason is `forwarded`/`mailing_list` |
| `fail` | everything else |

`forwarded` separates "the mail was mine and someone relayed it" from "someone
else is sending as me". Pass rate is `pass / total` by message count; the UI
shows `forwarded` separately, not as failure.

### Sources

A source is a group of IPs that belong to one sender:

1. User rules (`SOURCES_FILE`, optional YAML): name, matched by any of CIDR,
   PTR suffix, DKIM signing domain or ASN. First match wins.
2. Otherwise the AS organisation name from the ASN lookup.
3. Otherwise the registrable domain of a forward-confirmed PTR
   (public suffix list).
4. Otherwise the IP itself.

Rules are applied at query time, so editing the file regroups history
immediately (the file is reloaded on change).

### Enrichment

A background worker looks up each new source IP once: PTR with forward
confirmation through the system resolver, and origin ASN through Team Cymru's
DNS interface (`origin.asn.cymru.com`, `asn.cymru.com`). Results are cached for
30 days. Lookups are rate-limited, never block ingestion or page loads, and can
be disabled (`LOOKUPS=off`), in which case sources fall back to rules and IPs.

### Policy changes

The published policy seen per domain is tracked over time from reports. A
change in any field (`p`, `sp`, `np`, `pct`, alignment modes) becomes a marker on
the timeline with the first report that showed it.

### Time

Aggregate reports cover arbitrary windows, usually 24 hours not aligned to
midnight. A report's messages are counted on the day containing the window's
midpoint, in the configured `TIME_ZONE`. Windows longer than 48 hours are
flagged. Range filters use the same rule, so totals in every view agree.

## 8. User interface

React SPA embedded in the binary. Every filter (domain, range, source,
reporter, verdict) lives in the URL, so any view can be linked. Light and dark
themes follow the system. Works on a phone.

### Overview

- Domain picker (all domains by default) and range (7, 30, 90, 365 days,
  custom).
- Headline numbers: messages, pass rate, forwarded, failing, quarantined or
  rejected by receivers, reports received.
- Daily stacked bars by verdict, with policy-change markers.
- Failing and forwarded volume by source, top 10.
- New sources first seen in the range.
- Reporters that have gone quiet: reported before the range, nothing for 7 days.

### Sources

Table of sources: messages, pass/forwarded/fail split, SPF aligned %, DKIM
aligned %, DKIM domains seen, first and last seen. Sortable, filterable.

Source detail: IPs with PTR and ASN, daily volume, result combinations
(SPF result × DKIM result × alignment) with counts, envelope-from and DKIM
selectors seen, reporters that saw it, and the matching rule if any.

### Reports

List of reports: reporter, domain, window, messages, pass rate, duplicate or
conflict marker. Detail shows the published policy, every record with its auth
results, and links to download the extracted XML/JSON and the original message.

### TLS

Per policy domain: daily successful and failed sessions, failure types (for
example `certificate-expired`, `sts-policy-fetch-error`) with counts and the
MX hosts involved, and the policy type and string reported.

### Status

Per mailbox: UIDVALIDITY, high-water mark, last fetch start/end/result, next
scheduled fetch. Counts per message class. Messages that failed to parse or were
skipped, with the error and a download link for the raw message. Duplicates,
conflicts, UIDVALIDITY resets. Fetch now.

## 9. Interfaces

### HTTP

Read-only JSON under `/api`, aggregated in SQL so the client never loads raw
records. Pagination is cursor-based.

| Route | Purpose |
| --- | --- |
| `GET /api/domains` | Domains with first/last report dates |
| `GET /api/overview` | Headline numbers, daily series, top failing sources, new sources, quiet reporters |
| `GET /api/sources`, `/api/sources/{key}` | Source table and detail |
| `GET /api/reports`, `/api/reports/{id}` | Report list and detail |
| `GET /api/reports/{id}/raw`, `/api/messages/{id}/raw` | Downloads, `Content-Disposition: attachment` |
| `GET /api/tls`, `/api/tls/{domain}` | TLS report views |
| `GET /api/status` | Ingest state; includes `stale: true` when the last successful fetch is older than `STALE_AFTER` |
| `POST /api/fetch` | Fetch now; 202, 409 when running, 429 within the cooldown |
| `GET /healthz` | Process and database liveness |

`POST` requires `Sec-Fetch-Site: same-origin` (or the `X-Rua: 1` header for
scripts), so a third-party page cannot trigger fetches through a browser that
holds the client certificate. Responses set a strict CSP (`default-src 'self'`),
`X-Content-Type-Options: nosniff` and `Referrer-Policy: no-referrer`.

### Metrics

On `METRICS_ADDR`: last successful fetch timestamp, fetch duration and errors,
messages by class and parse status, reports ingested, records by domain and
verdict, enrichment queue length. No IPs or source names as labels.

### CLI

| Command | Purpose |
| --- | --- |
| `rua serve` | Server, fetch loop and enrichment (default) |
| `rua fetch` | One fetch and exit; for cron-style use |
| `rua import <path>...` | Local files |
| `rua reparse` | Reparse all stored blobs now |
| `rua demo` | Serve a generated, realistic dataset in memory; used for screenshots, e2e and trying the UI |

## 10. Configuration

Environment variables; secrets may come from `<NAME>_FILE`.

| Variable | Default | Purpose |
| --- | --- | --- |
| `IMAP_ADDR` | empty | `host:port`; IMAP disabled when empty |
| `IMAP_TLS` | `tls` | `tls`, `starttls`, `none` |
| `IMAP_CA_FILE` | empty | Extra trust root for the IMAP connection |
| `IMAP_USERNAME`, `IMAP_PASSWORD` | required with `IMAP_ADDR` | |
| `IMAP_MAILBOXES` | `INBOX` | Comma-separated |
| `FETCH_INTERVAL` | `30m` | |
| `STALE_AFTER` | `3h` | Status and metric threshold |
| `MAX_MESSAGE_BYTES` | `26214400` | |
| `DB_PATH` | `./rua.db` | |
| `RETENTION_DAYS` | `0` | 0 keeps everything |
| `SOURCES_FILE` | empty | Optional source rules |
| `LOOKUPS` | `on` | PTR and ASN enrichment |
| `TIME_ZONE` | `UTC` | Day boundaries |
| `LISTEN_ADDR` | `:8080` | |
| `METRICS_ADDR` | `:9090` | Empty disables |
| `LOG_LEVEL` | `INFO` | |

## 11. Testing

- Parser fixtures: real-world aggregate reports from major receivers
  (anonymised), DMARCbis samples, TLS reports, failure reports, malformed and
  hostile inputs (zip bombs, deep nesting, oversized strings, invalid UTF-8).
  Go fuzz targets for XML, JSON and MIME extraction.
- IMAP against an in-process server (`go-imap/v2` `imapserver` with `imapmemserver`): incremental
  fetch, crash between messages, UIDVALIDITY reset, oversized messages, missing
  mailbox, and a guard that the server saw no `SELECT`, `STORE`, `COPY`,
  `MOVE`, `EXPUNGE` or `APPEND`.
- Query tests: verdicts, day bucketing across time zones, duplicates, source
  rules.
- UI: unit tests for formatting and URL state; Playwright against `rua demo`
  for every page on desktop and phone widths, both themes.

## 12. Decisions

| Decision | Why |
| --- | --- |
| Long-running server with an internal loop, not a CronJob | One process owns the database; no shared volume between a writer job and a web server; incremental state lives next to the data |
| SQLite, not static files | Arbitrary grouping and ranges; derived views are queries, not regenerated pages |
| Keep raw blobs | Parser fixes and new views without refetching from IMAP; downloadable evidence |
| `EXAMINE` only | The mailbox is the archive and is backed up by Mailvault; a viewer has no reason to write to it |
| No merging at ingest | Merging loses per-reporter and per-day detail; queries aggregate instead |
| Receiver's evaluated result is authoritative | rua cannot re-verify signatures; it classifies what receivers saw |
| No auth | Authentication is the deployment's job; mTLS in the homelab, a proxy elsewhere |
| ASN over GeoIP | Names the sender (Google, Amazon SES, a VPS host), needs no licensed database |
