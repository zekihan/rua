# rua: Homelab Rollout

Status — done (2026-11-25)

How rua replaces `dmarc-report-converter` in `zekihan/homelab`. Paths are
relative to that repository.

## Repository

- `terraform/git/repository.tf`: `zekihan/rua` on GitHub (public, Docker Hub
  secrets), GitLab and Forgejo, like `basket`. Operator apply.
- `apps/git-mirrorer/values.yaml`: mirror entry to GitLab and Forgejo with the
  `refs/pull/` mapping.

## Chart: `apps/dmarc`

The application keeps its name, namespace, host and mTLS identity so DNS,
certificates, `libs/mtls-policy` and Gatus need no rename.

| Now | After |
| --- | --- |
| `converter` CronJob + `dmarc` nginx Deployment | One `dmarc` Deployment running `rua serve`, 1 replica, `Recreate` |
| SMB PV/PVC on TrueNAS, rebuilt every run | 1Gi Longhorn RWO PVC at `/data`, no backup annotation (rebuildable from the mailbox) |
| Converter config Secret templated into YAML | ExternalSecret with `IMAP_USERNAME`, `IMAP_PASSWORD` from the same SOPS bundle; SMB keys unused |
| Global `SSL_CERT_FILE` | `IMAP_CA_FILE=/etc/bridge-ca/ca.crt` from the `bridge-ca` ConfigMap |
| oauth2-proxy admin middleware + mTLS | mTLS only on cluster-1; the oauth2-proxy middleware stays as the rollback default for a disabled `mtlsAuthz`, like the other mTLS consoles |
| Marks mail read | `EXAMINE` only |

Values: `IMAP_ADDR=mailvault-mailvault-bridge.mailvault.svc.cluster.local:1143`,
`IMAP_TLS=starttls`, `IMAP_MAILBOXES=Folders/dmarc`, `STALE_AFTER=3h`,
`SOURCES_FILE=/config/sources.yaml` from a chart ConfigMap that names and marks
as expected the known senders (Proton, Amazon SES for `postmaster`,
Purelymail). The time zone is chosen in the UI. Pod egress: DNS and the
Bridge on 1143; PTR and Team Cymru lookups go through kube-dns. Ingress from
Traefik only; the metrics port from vmagent only.

`apps/mailvault/templates/dmarc-network.yaml` changes its pod selector from
`app.kubernetes.io/controller: converter` to `dmarc`.

## Monitoring

- `platform/argus/values.cluster-1.yaml`: drop the `dmarc/dmarc-converter`
  CronJob freshness item.
- `apps/gatus/config/argus.yaml`: drop "Dmarc converter freshness".
- `apps/gatus/config/admin.yaml`: replace the `HEAD /` check with
  `GET /api/status` and conditions `[STATUS] == 200`, `[BODY].stale == false`.
  Gatus already holds the `gatus-cluster-1` client certificate.

## Retirement

Done after the backfill (595 messages, all parsed):

- `apps/dmarc/renovate.deps`: dropped the `tierpod/dmarc-report-converter` entry.
- `zekihan/dockerfiles`: removed `dmarc-report-converter/`, its tests and its
  publish jobs. Published tags stay in the registries.
- `terraform/truenas/dataset_dmarc.tf`: removed the share, ACL and dataset.
  TrueNAS refuses to delete a non-empty dataset, so `force_destroy = true` was
  applied first (a state-only update) and the resource removed after.
- `zekihan/secrets`: dropped the unused `SMB_*` and `IMAP_SERVER` keys from the
  `dmarc` bundles.

## What happened

1. Repositories created (terraform/git), rua implemented, `v0.1.0` and an
   edge image released.
2. One push switched the chart, the mailvault Bridge policy, Gatus and Argus.
   dmarc kept `autoSync: true`: the homelab tests fix the set of manual
   applications, and rua never writes to the mailbox, so a revert is the
   rollback.
3. mailvault and argus are manual applications and were synced by hand; the
   first rua fetch timed out until the Bridge policy was applied, and a Fetch
   now completed the backfill.
4. Every Application has `Prune=false`, so the converter CronJob, its config,
   the old PDB, network policy, ExternalSecrets and the SMB PV/PVC were deleted
   by hand. The AppProject changes went through a selective sync of the root.
