import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, messageRawHref, ApiError } from "../api/client.ts";
import type { PageProps } from "../page-props.ts";
import { Loading, ErrorBox, Empty } from "../components/ui.tsx";
import { formatNumber, formatBytes, formatUnixDateTime } from "../format.ts";
import type { MessageRow } from "../api/types.ts";

function MessageTable({ rows, testid }: { rows: MessageRow[]; testid: string }) {
  if (!rows.length) return <Empty>None.</Empty>;
  return <div className="table-wrap"><table data-testid={testid}><thead><tr><th>Date</th><th>From / subject</th><th>Mailbox</th><th>Class / status</th><th>Error</th><th>Download</th></tr></thead><tbody>{rows.map((m) => <tr key={m.id}><td className="small">{formatUnixDateTime(m.internalDateAt, "UTC")}<div className="muted">{formatBytes(m.size)} · UID {m.uid}</div></td><td>{m.from}<div className="small muted">{m.subject}</div></td><td>{m.mailbox || "—"}</td><td>{m.class} / {m.status}</td><td className="small">{m.error || "—"}</td><td>{m.hasRaw ? <a href={messageRawHref(m.id)} download>Raw message</a> : "—"}</td></tr>)}</tbody></table></div>;
}

export default function StatusPage(p: PageProps) {
  const qc = useQueryClient();
  const q = useQuery({ queryKey: ["status"], queryFn: () => api.status(), refetchInterval: 30000 });
  const mut = useMutation({ mutationFn: () => api.fetchNow(), onSettled: () => void qc.invalidateQueries({ queryKey: ["status"] }) });
  if (q.isLoading) return <Loading />;
  if (q.error) return <ErrorBox error={q.error} />;
  const d = q.data!;
  const nowSec = Date.now() / 1000;
  const cooldown = d.fetch.cooldownUntilAt !== null && d.fetch.cooldownUntilAt > nowSec;
  const disabled = !d.fetch.enabled || d.fetch.running || cooldown || mut.isPending;
  const fetchMsg = mut.isSuccess ? "Fetch started." : mut.error ? (mut.error instanceof ApiError ? (mut.error.status === 409 ? "A fetch is already running." : mut.error.status === 429 ? `Fetch is in cooldown${d.fetch.cooldownUntilAt ? ` until ${formatUnixDateTime(d.fetch.cooldownUntilAt, p.tz)}` : ""}.` : mut.error.message) : String(mut.error)) : "";
  return <div style={{ display: "grid", gap: 14 }}>
    <h1 data-testid="status-heading">Status</h1>
    {d.stale && <div role="alert" data-testid="stale-banner" className="card" style={{ borderColor: "var(--fail)" }}><strong>Stale:</strong> no successful fetch within {formatNumber(d.staleAfterSeconds)} seconds. Last success: {formatUnixDateTime(d.lastSuccessAt, p.tz)}.</div>}
    <section className="card"><h2>Fetch</h2>
      <p className="small muted">Enabled: {d.fetch.enabled ? "yes" : "no"} · Running: {d.fetch.running ? "yes" : "no"} · Next fetch: {formatUnixDateTime(d.fetch.nextFetchAt, p.tz)} · Cooldown until: {formatUnixDateTime(d.fetch.cooldownUntilAt, p.tz)} · Parser version: {d.parserVersion} · Reparsing: {d.reparsing ? "yes" : "no"} · Enrichment queue: {d.enrichQueue} · Duplicates: {formatNumber(d.duplicates)}</p>
      <button type="button" className="btn" data-testid="fetch-now" disabled={disabled} onClick={() => mut.mutate()}>Fetch now</button>
      {fetchMsg && <p role="status" data-testid="fetch-message" style={{ marginTop: 8 }}>{fetchMsg}</p>}
      {!d.fetch.enabled && <p className="small muted">Fetching is disabled (IMAP not configured).</p>}
    </section>
    <section className="card"><h2>Mailboxes</h2>{d.mailboxes.length === 0 ? <Empty>No mailboxes configured.</Empty> : <div className="table-wrap"><table data-testid="mailboxes-table"><thead><tr><th>Name</th><th>UIDVALIDITY</th><th>Next UID</th><th>Last fetch</th><th>Result</th></tr></thead><tbody>{d.mailboxes.map((m) => <tr key={m.name}><td>{m.name}</td><td className="num">{m.uidValidity}</td><td className="num">{m.nextUid}</td><td className="small">{formatUnixDateTime(m.lastFetchStartAt, p.tz)} – {formatUnixDateTime(m.lastFetchEndAt, p.tz)}<div className="muted">last success {formatUnixDateTime(m.lastSuccessAt, p.tz)}</div></td><td>{m.lastResult || "—"}{m.lastError && <div className="small muted">{m.lastError}</div>}</td></tr>)}</tbody></table></div>}</section>
    <section className="card"><h2>Message counts</h2>{d.classes.length === 0 ? <Empty>No messages yet.</Empty> : <div className="table-wrap"><table data-testid="classes-table"><thead><tr><th>Class</th><th>Status</th><th>Messages</th></tr></thead><tbody>{d.classes.map((c, i) => <tr key={i}><td>{c.class}</td><td>{c.status}</td><td className="num">{formatNumber(c.messages)}</td></tr>)}</tbody></table></div>}</section>
    <section className="card"><h2>Problem messages</h2><MessageTable rows={d.problems} testid="problems-table" /></section>
    <section className="card"><h2>Other messages</h2><MessageTable rows={d.other} testid="other-messages-table" /></section>
    <section className="card"><h2>Conflicting reports</h2>{d.conflicts.length === 0 ? <Empty>No conflicts.</Empty> : <ul>{d.conflicts.map((c) => <li key={c.id}><a href={p.makeHref("report", { id: String(c.id) })}>#{c.id} {c.org} — {c.domain}</a> <span className="badge badge-fail">conflict</span></li>)}</ul>}</section>
    <section className="card"><h2>Events</h2>{d.events.length === 0 ? <Empty>No events.</Empty> : <div className="table-wrap"><table data-testid="events-table"><thead><tr><th>At</th><th>Kind</th><th>Mailbox</th><th>Detail</th></tr></thead><tbody>{d.events.map((e, i) => <tr key={i}><td className="small">{formatUnixDateTime(e.at, p.tz)}</td><td>{e.kind === "uidvalidity-reset" ? <span className="badge">uidvalidity-reset</span> : e.kind}</td><td>{e.mailbox}</td><td className="small">{e.detail}</td></tr>)}</tbody></table></div>}</section>
    <section className="card"><h2>Failure reports</h2>{d.failureReports.length === 0 ? <Empty>No failure reports.</Empty> : <div className="table-wrap"><table data-testid="failure-reports-table"><thead><tr><th>Arrival</th><th>Domain</th><th>Source IP</th><th>Auth failure</th><th>Headers (From / To / Subject)</th><th>Message</th></tr></thead><tbody>{d.failureReports.map((f, i) => <tr key={i}><td className="small">{formatUnixDateTime(f.arrivalAt, p.tz)}</td><td>{f.domain}</td><td className="mono">{f.sourceIp}</td><td>{f.authFailure}</td><td className="small">{f.from} / {f.to} / {f.subject}</td><td>{f.messageId > 0 ? <a href={messageRawHref(f.messageId)} download>Raw</a> : "—"}</td></tr>)}</tbody></table></div>}</section>
  </div>;
}
