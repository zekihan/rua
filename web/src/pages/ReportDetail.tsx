import { useQuery } from "@tanstack/react-query";
import { api, reportRawHref, messageRawHref } from "../api/client.ts";
import type { PageProps } from "../page-props.ts";
import { Loading, ErrorBox, Empty, VerdictBadge } from "../components/ui.tsx";
import { formatNumber, formatPercent, formatUnixDateTime } from "../format.ts";

export default function ReportDetail(p: PageProps) {
  const id = p.state.id;
  const q = useQuery({ queryKey: ["report", id, p.tz], queryFn: () => api.report(id, p.tz) });
  if (q.isLoading) return <Loading />;
  if (q.error) return <ErrorBox error={q.error} />;
  const d = q.data!; const r = d.report;
  return (
    <div style={{ display: "grid", gap: 14 }}>
      <h1 data-testid="report-detail-heading">Report #{r.id}</h1>
      <p className="muted">{r.kind} · {r.org} · {r.domain} · report ID <span className="mono">{r.reportId || "—"}</span> · {formatUnixDateTime(r.begin, p.tz)} – {formatUnixDateTime(r.end, p.tz)} {r.duplicate && <span className="badge">duplicate</span>} {r.conflict && <span className="badge badge-fail">conflict</span>}</p>
      <div style={{ display: "flex", gap: 10, flexWrap: "wrap" }}>
        <a className="btn" data-testid="report-raw-download" href={reportRawHref(r.id)} download>Download extracted report</a>
        {d.messageId > 0 && <a className="btn btn-secondary" data-testid="message-raw-download" href={messageRawHref(d.messageId)} download>Download original message</a>}
      </div>
      <section className="card"><h2>Published policy</h2>{d.policy ? <dl style={{ display: "grid", gridTemplateColumns: "repeat(auto-fit,minmax(120px,1fr))", gap: 8 }}>{(["p", "sp", "np", "pct", "adkim", "aspf", "fo", "testing", "discovery"] as const).map((k) => <div key={k}><dt className="small muted">{k}</dt><dd className="mono">{k === "pct" ? (d.policy!.pct ?? "—") : (d.policy![k] || "—")}</dd></div>)}</dl> : <Empty>No published policy in this report.</Empty>}
        <p className="small muted">Reporter email: {d.email || "—"}{d.extraContact ? ` · extra: ${d.extraContact}` : ""}{d.schema ? ` · schema: ${d.schema}` : ""}</p></section>
      {r.kind !== "tls-report" && <section className="card"><h2>Records</h2>{d.records.length === 0 ? <Empty>No records.</Empty> : <div className="table-wrap"><table data-testid="report-records-table"><thead><tr><th>Source</th><th>Count</th><th>Disposition</th><th>SPF / DKIM</th><th>Verdict</th><th>Auth results</th></tr></thead><tbody>{d.records.map((rec, i) => <tr key={i}><td className="mono">{rec.sourceIp}<div className="small"><a href={p.makeHref("source", { key: rec.sourceKey })}>{rec.sourceName || rec.sourceKey}</a></div><div className="small muted">from {rec.envelopeFrom || "—"} → {rec.envelopeTo || "—"}; header-from {rec.headerFrom || "—"}</div></td><td className="num">{formatNumber(rec.count)}</td><td>{rec.disposition}</td><td>SPF {rec.spf} · DKIM {rec.dkim}</td><td><VerdictBadge verdict={rec.verdict} />{rec.reasons.map((x, j) => <div key={j} className="small muted">{x.type}{x.comment ? `: ${x.comment}` : ""}</div>)}</td><td>{rec.auth.length ? rec.auth.map((a, j) => <div key={j} className="small mono">{a.method}: {a.domain || "—"}{a.selector ? ` (${a.selector})` : ""}{a.scope ? ` [${a.scope}]` : ""} = {a.result}</div>) : "—"}</td></tr>)}</tbody></table></div>}</section>}
      {d.tlsPolicies.length > 0 && <section className="card"><h2>TLS policies</h2><div className="table-wrap"><table data-testid="report-tls-policies-table"><thead><tr><th>Type / domain</th><th>Policy strings</th><th>MX hosts</th><th>Success / failure</th><th>Failure details</th></tr></thead><tbody>{d.tlsPolicies.map((pol, i) => <tr key={i}><td>{pol.type} · {pol.domain}</td><td className="mono small">{pol.strings.join(" ") || "—"}</td><td className="mono small">{pol.mxHosts.join(", ") || "—"}</td><td className="num">{formatNumber(pol.success)} / {formatNumber(pol.failure)}</td><td>{pol.failures.length ? pol.failures.map((f, j) => <div key={j} className="small">{f.resultType}: {formatNumber(f.failedSessions)} failed ({f.sendingIp || "—"} → {f.receivingMx || f.receivingIp || "—"}{f.reasonCode ? `, ${f.reasonCode}` : ""})</div>) : "—"}</td></tr>)}</tbody></table></div></section>}
      <section className="card"><h2>Copies</h2>{d.copies.length === 0 ? <Empty>No other copies.</Empty> : <ul>{d.copies.map((c) => <li key={c.id}><a href={p.makeHref("report", { id: String(c.id) })}>#{c.id} {c.org}</a> <span className="small muted">{formatPercent(c.passRate)} pass{c.duplicate ? ", duplicate" : ""}{c.conflict ? ", conflict" : ""}</span></li>)}</ul>}</section>
    </div>
  );
}
