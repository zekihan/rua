import { useQuery } from "@tanstack/react-query";
import { api } from "../api/client.ts";
import type { PageProps } from "../page-props.ts";
import { Loading, ErrorBox, Empty } from "../components/ui.tsx";
import { TLSBars } from "../components/Charts.tsx";
import { formatNumber, formatUnixDateTime } from "../format.ts";

export function TlsList(p: PageProps) {
  const q = useQuery({ queryKey: ["tls", p.eff], queryFn: () => api.tls(p.eff) });
  if (q.isLoading) return <Loading />;
  if (q.error) return <ErrorBox error={q.error} />;
  const rows = q.data!.domains;
  return <div style={{ display: "grid", gap: 12 }}><h1 data-testid="tls-heading">TLS reports</h1>
    {rows.length === 0 ? <Empty>No TLS reports in this range.</Empty> : <div className="card table-wrap"><table data-testid="tls-domains-table"><thead><tr><th>Domain</th><th>Success</th><th>Failure</th><th>Reports</th><th>Last report</th></tr></thead><tbody>{rows.map((d) => <tr key={d.domain}><td><a href={p.makeHref("tlsDomain", { key: d.domain })}>{d.domain}</a></td><td className="num">{formatNumber(d.success)}</td><td className="num">{formatNumber(d.failure)}</td><td className="num">{formatNumber(d.reports)}</td><td>{formatUnixDateTime(d.lastReportAt, p.tz)}</td></tr>)}</tbody></table></div>}</div>;
}

export function TlsDomain(p: PageProps) {
  const domain = p.state.key;
  const q = useQuery({ queryKey: ["tlsDomain", domain, p.eff], queryFn: () => api.tlsDomain(domain, p.eff) });
  if (q.isLoading) return <Loading />;
  if (q.error) return <ErrorBox error={q.error} />;
  const d = q.data!;
  return <div style={{ display: "grid", gap: 14 }}>
    <h1 data-testid="tls-domain-heading">{d.domain}</h1>
    <p className="muted">Successful sessions {formatNumber(d.success)} · failed {formatNumber(d.failure)}</p>
    <section className="card"><h2>Daily sessions</h2><TLSBars days={d.days} /></section>
    <section className="card"><h2>Failure types</h2>{d.failures.length === 0 ? <Empty>No failures in this range.</Empty> : <div className="table-wrap"><table data-testid="tls-failures-table"><thead><tr><th>Result type</th><th>Sessions</th><th>MX hosts</th><th>Receiving IPs</th><th>Reason codes</th></tr></thead><tbody>{d.failures.map((f) => <tr key={f.resultType}><td className="mono">{f.resultType}</td><td className="num">{formatNumber(f.sessions)}</td><td className="mono small">{f.mxHosts.join(", ") || "—"}</td><td className="mono small">{f.receivingIps.join(", ") || "—"}</td><td className="mono small">{f.reasonCodes.join(", ") || "—"}</td></tr>)}</tbody></table></div>}</section>
    <section className="card"><h2>Policies</h2>{d.policies.length === 0 ? <Empty>No policies in this range.</Empty> : <div className="table-wrap"><table data-testid="tls-policies-table"><thead><tr><th>Type</th><th>Domain</th><th>Policy strings</th><th>MX hosts</th><th>Success / failure</th><th>Reports</th></tr></thead><tbody>{d.policies.map((pol, i) => <tr key={i}><td>{pol.type}</td><td>{pol.domain}</td><td className="mono small">{pol.strings.join(" ") || "—"}</td><td className="mono small">{pol.mxHosts.join(", ") || "—"}</td><td className="num">{formatNumber(pol.success)} / {formatNumber(pol.failure)}</td><td className="num">{formatNumber(pol.reports)}</td></tr>)}</tbody></table></div>}</section>
    <section className="card"><h2>Reporters</h2>{d.reporters.length === 0 ? <Empty>No reporters.</Empty> : <ul>{d.reporters.map((r, i) => <li key={i}>{r.value} <span className="muted small">{formatNumber(r.messages)} sessions</span></li>)}</ul>}</section>
  </div>;
}
