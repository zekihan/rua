# rua: Homelab Rollout

Status — draft

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
| oauth2-proxy admin middleware + mTLS | mTLS only (`mtlsAuthz.enabled: true`), no oauth2 middleware |
| Marks mail read | `EXAMINE` only |

Values: `IMAP_ADDR=mailvault-mailvault-bridge.mailvault.svc.cluster.local:1143`,
`IMAP_TLS=starttls`, `IMAP_MAILBOXES=Folders/dmarc`, `TIME_ZONE=Europe/Istanbul`,
`STALE_AFTER=3h`. Pod egress: DNS, the Bridge on 1143 and public DNS
resolvers only if the cluster resolver cannot answer Team Cymru queries
(it can, through kube-dns, so no extra egress). Ingress from Traefik only;
metrics port from the monitoring namespace.

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

After rua has backfilled and the Status page shows no unexpected parse
failures:

- `apps/dmarc/renovate.deps`: drop the `tierpod/dmarc-report-converter` entry.
- `zekihan/dockerfiles`: remove `dmarc-report-converter/`, its tests and its
  publish matrix entry.
- `terraform/truenas/dataset_dmarc.tf`: remove the share and dataset
  (operator apply, after the PV is gone).

## Order

1. Create the repositories (terraform/git), implement rua, release `v0.1.0`
   and an edge image.
2. Chart change with `autoSync: false`; sync manually, watch the backfill on
   the Status page, compare per-domain totals with the old HTML output for a
   few known reports.
3. Gatus, Argus and mailvault network policy changes.
4. Re-enable auto-sync, then the retirement steps.
