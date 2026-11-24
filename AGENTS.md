# AGENTS.md

Guidance for AI agents working in this repository.

## Documents

- `docs/DESIGN.md` — behavior, data model and interfaces. The source of truth;
  its Decisions table records why.
- `docs/TECH_STACK.md` — technology choices and layout.
- `docs/HOMELAB.md` — how the homelab deployment replaces
  `dmarc-report-converter`.

## Related repositories (local clones)

- `../basket`, `../family-tab`: template for Makefile, goreleaser, Dockerfile,
  workflows, web build and Playwright setup.
- `../mailvault`: `go-imap/v2` usage against Proton Bridge.
- `../release-workflows`: shared decide/image/release workflows, pinned by SHA.
- `../homelab`: the deployment (`apps/dmarc`).

## Rules

- The mailbox is never modified. Only the read-only IMAP interface in
  `internal/imapsrc` talks to the server; do not widen it.
- Report content is untrusted: cap lengths, never interpolate it into
  commands, render it escaped.
- Aggregation happens in queries, never at ingest. Stored records stay one row
  per report record.
- Changing parsing behavior bumps the parser version so stored blobs reparse.
- The repository is public: fixtures must be anonymised, and nothing
  homelab-specific belongs in code or defaults.
- Commits: short imperative subject, optional body, one logical change per
  commit. Run lint and tests before committing. Do not push, tag or publish
  without explicit instruction.
