import { useInfiniteQuery } from "@tanstack/react-query";
import { api } from "../api/client.ts";
import type { PageProps } from "../page-props.ts";
import { Loading, ErrorBox, Empty } from "../components/ui.tsx";
import { formatNumber, formatPercent, formatUnixDateTime } from "../format.ts";

export default function Reports(p: PageProps) {
  const f = p.state.filters;
  const q = useInfiniteQuery({
    queryKey: ["reports", p.eff, f.reporter, f.kind, f.verdict, f.source],
    queryFn: ({ pageParam }) => api.reports({ ...p.eff, reporter: f.reporter, kind: f.kind, verdict: f.verdict, source: f.source, cursor: pageParam, limit: 50 }),
    initialPageParam: "",
    getNextPageParam: (last) => last.nextCursor || undefined,
  });
  if (q.isLoading) return <Loading />;
  if (q.error) return <ErrorBox error={q.error} />;
  const rows = q.data?.pages.flatMap((x) => x.reports) ?? [];
  const patch = (patchObj: Partial<typeof f>) => p.navigate(p.makeHref("reports", { patch: patchObj }));
  return (
    <div style={{ display: "grid", gap: 12 }}>
      <h1 data-testid="reports-heading">Reports</h1>
      <div style={{ display: "flex", gap: 10, flexWrap: "wrap" }}>
        <label>Reporter <input className="input" data-testid="reports-reporter-filter" defaultValue={f.reporter} onBlur={(e) => patch({ reporter: e.target.value })} onKeyDown={(e) => { if (e.key === "Enter") patch({ reporter: (e.target as HTMLInputElement).value }); }} /></label>
        <label>Kind <select className="input" data-testid="reports-kind-filter" value={f.kind} onChange={(e) => patch({ kind: e.target.value })}><option value="">All kinds</option><option value="dmarc-aggregate">dmarc-aggregate</option><option value="tls-report">tls-report</option></select></label>
        <label>Verdict <select className="input" data-testid="reports-verdict-filter" value={f.verdict} onChange={(e) => patch({ verdict: e.target.value })}><option value="">Any verdict</option><option value="pass">pass</option><option value="forwarded">forwarded</option><option value="fail">fail</option></select></label>
      </div>
      {rows.length === 0 ? <Empty>No reports in this range.</Empty> :
      <div className="card table-wrap"><table data-testid="reports-table"><thead><tr><th>Reporter</th><th>Domain</th><th>Window</th><th>Messages</th><th>Pass rate / TLS failed</th><th>Markers</th></tr></thead><tbody>
        {rows.map((r) => <tr key={r.id}><td><a href={p.makeHref("report", { id: String(r.id) })}>{r.org || r.reportId || `#${r.id}`}</a><div className="small muted">{r.kind} · {r.date}</div></td><td>{r.domain}</td><td className="small">{formatUnixDateTime(r.begin, p.tz)} – {formatUnixDateTime(r.end, p.tz)}</td><td className="num">{formatNumber(r.messages)}</td><td className="num">{r.kind === "tls-report" ? formatNumber(r.tlsFailed) : formatPercent(r.passRate)}</td><td>{r.duplicate && <span className="badge">duplicate{r.duplicateOf ? ` of #${r.duplicateOf}` : ""}</span>} {r.conflict && <span className="badge badge-fail">conflict</span>} {r.longWindow && <span className="badge">long window</span>}</td></tr>)}
      </tbody></table></div>}
      {q.hasNextPage && <button type="button" className="btn" data-testid="reports-load-more" disabled={q.isFetchingNextPage} onClick={() => void q.fetchNextPage()}>{q.isFetchingNextPage ? "Loading…" : "Load more"}</button>}
    </div>
  );
}
