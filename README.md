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

Status: design. See [docs/DESIGN.md](docs/DESIGN.md) and
[docs/TECH_STACK.md](docs/TECH_STACK.md).

## License

AGPL-3.0-only.
