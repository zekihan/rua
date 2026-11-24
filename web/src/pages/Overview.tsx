import { useQuery } from "@tanstack/react-query";
import { api } from "../api/client.ts";
import type { PageProps } from "../page-props.ts";
import { Loading, ErrorBox, Empty, Legend } from "../components/ui.tsx";
import { StackedBars, Donut } from "../components/Charts.tsx";
import { formatNumber, formatPercent, formatUnixDateTime, formatDateLabel } from "../format.ts";

export default function Overview(p: PageProps) {
  const q = useQuery({ queryKey: ["overview", p.eff], queryFn: () => api.overview(p.eff) });
  if (q.isLoading) return <Loading />;
  if (q.error) return <ErrorBox error={q.error} />;
  const d = q.data!;
  const t = d.totals;
  const stats: [string, string, string][] = [
    ["messages", "Messages", formatNumber(t.messages)], ["pass-rate", "Pass rate", formatPercent(t.passRate)],
    ["forwarded", "Forwarded", formatNumber(t.forwarded)], ["failing", "Failing", formatNumber(t.fail)],
    ["quarantined", "Quarantined", formatNumber(t.quarantined)], ["rejected", "Rejected", formatNumber(t.rejected)], ["reports", "Reports received", formatNumber(t.reports)],
  ];
  return (
    <div style={{ display: "grid", gap: 16 }}>
      <h1 data-testid="overview-heading">Overview</h1>
      <section aria-label="Headline numbers" style={{ display: "grid", gridTemplateColumns: "repeat(auto-fit,minmax(130px,1fr))", gap: 10 }}>
        {stats.map(([id, label, v]) => <div key={id} className="card" data-testid={`stat-${id}`}><div className="small muted">{label}</div><div className="num" style={{ fontSize: "1.4rem", fontWeight: 700 }}>{v}</div></div>)}
        <div className="card"><div className="small muted">Verdict split</div><Donut pass={t.pass} forwarded={t.forwarded} fail={t.fail} size={72} /></div>
      </section>
      {t.messages === 0 ? <Empty>No reports in this range.</Empty> : <>
        <section className="card"><h2>Daily messages by verdict</h2><Legend /><StackedBars days={d.days} policyChanges={d.policyChanges} />
          {d.policyChanges.length > 0 && <div data-testid="policy-changes"><h3>Policy changes</h3><ul>{d.policyChanges.map((c, i) => <li key={i} className="small"><a href={p.makeHref("report", { id: String(c.reportId) })}>{formatDateLabel(c.date, p.tz)} — {c.domain}</a>: changed {c.fields.join(", ") || "policy"} <span className="muted">(before: {c.before ? `${c.before.p || "—"} pct ${c.before.pct ?? "—"}` : "none"} → after: {c.after.p || "—"} pct {c.after.pct ?? "—"})</span></li>)}</ul></div>}
        </section>
        {d.longWindows > 0 && <p className="small muted" data-testid="long-windows-note">{formatNumber(d.longWindows)} report(s) in this range cover windows longer than 48 hours; their messages are counted on the day containing the window midpoint.</p>}
        <section className="card"><h2>Top failing and forwarded sources</h2>{d.topFailing.length === 0 ? <Empty>No failing or forwarded sources in this range.</Empty> :
          <div className="table-wrap"><table data-testid="top-failing-table"><thead><tr><th>Source</th><th>Messages</th><th>Forwarded</th><th>Fail</th></tr></thead><tbody>{d.topFailing.map((s) => <tr key={s.key} className={s.expected ? "" : "unknown-row"}><td><a href={p.makeHref("source", { key: s.key })}>{s.name || s.key}</a> {s.expected ? "" : <span className="badge">unknown</span>}</td><td className="num">{formatNumber(s.messages)}</td><td className="num">{formatNumber(s.forwarded)}</td><td className="num">{formatNumber(s.fail)}</td></tr>)}</tbody></table></div>}</section>
        <div style={{ display: "grid", gridTemplateColumns: "repeat(auto-fit,minmax(280px,1fr))", gap: 12 }}>
          <section className="card"><h2>New unknown sources</h2>{d.newUnknown.length === 0 ? <Empty>No new unknown sources in this range.</Empty> : <ul>{d.newUnknown.map((s) => <li key={s.key}><a href={p.makeHref("source", { key: s.key })}>{s.name || s.key}</a> <span className="small muted">first seen {s.firstSeen}, {formatNumber(s.messages)} messages (pass {formatNumber(s.pass)}, forwarded {formatNumber(s.forwarded)}, fail {formatNumber(s.fail)})</span></li>)}</ul>}</section>
          <section className="card"><h2>New expected sources</h2>{d.newExpected.length === 0 ? <Empty>No new expected sources in this range.</Empty> : <ul>{d.newExpected.map((s) => <li key={s.key}><a href={p.makeHref("source", { key: s.key })}>{s.name || s.key}</a> <span className="small muted">first seen {s.firstSeen}, {formatNumber(s.messages)} messages</span></li>)}</ul>}</section>
          <section className="card"><h2>Quiet reporters</h2>{d.quietReporters.length === 0 ? <Empty>No quiet reporters.</Empty> : <ul>{d.quietReporters.map((r) => <li key={r.org}>{r.org} <span className="small muted">last {formatUnixDateTime(r.lastReportAt, p.tz)}; domains: {r.domains.join(", ")}</span></li>)}</ul>}</section>
        </div>
      </>}
    </div>
  );
}
