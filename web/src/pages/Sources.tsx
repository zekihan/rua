import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { api } from "../api/client.ts";
import type { PageProps } from "../page-props.ts";
import { Loading, ErrorBox, Empty } from "../components/ui.tsx";
import { SplitBar } from "../components/Charts.tsx";
import { formatNumber, formatPercent } from "../format.ts";
import type { SourceRow } from "../api/types.ts";

type SortKey = "name" | "messages" | "spfAligned" | "dkimAligned" | "firstSeen" | "lastSeen";
export default function Sources(p: PageProps) {
  const q = useQuery({ queryKey: ["sources", p.eff], queryFn: () => api.sources(p.eff) });
  const [sort, setSort] = useState<SortKey>("messages");
  const [dir, setDir] = useState(-1);
  const [filter, setFilter] = useState("");
  const rows = useMemo(() => {
    let r = (q.data?.sources ?? []).slice();
    if (filter) { const f = filter.toLowerCase(); r = r.filter((s) => [s.name, s.key, ...s.dkimDomains].join(" ").toLowerCase().includes(f)); }
    r.sort((a, b) => { const av = a[sort as keyof SourceRow]; const bv = b[sort as keyof SourceRow]; const cmp = typeof av === "number" && typeof bv === "number" ? av - bv : String(av ?? "").localeCompare(String(bv ?? "")); return cmp * dir; });
    return r;
  }, [q.data, sort, dir, filter]);
  if (q.isLoading) return <Loading />;
  if (q.error) return <ErrorBox error={q.error} />;
  const toggle = (k: SortKey) => { if (sort === k) setDir(dir * -1); else { setSort(k); setDir(-1); } };
  const Th = ({ k, children }: { k: SortKey; children: string }) => <th><button type="button" onClick={() => toggle(k)} aria-label={`Sort by ${children}`} style={{ background: "none", border: 0, font: "inherit", cursor: "pointer", padding: 0 }}>{children}{sort === k ? (dir < 0 ? " ↓" : " ↑") : ""}</button></th>;
  return (
    <div style={{ display: "grid", gap: 12 }}>
      <h1 data-testid="sources-heading">Sources</h1>
      <div style={{ display: "flex", gap: 12, flexWrap: "wrap", alignItems: "center" }}>
        <label>Filter <input className="input" data-testid="sources-filter" value={filter} onChange={(e) => setFilter(e.target.value)} placeholder="name or DKIM domain" /></label>
        <label><input type="checkbox" data-testid="unknown-only-toggle" checked={p.state.filters.unknownOnly} onChange={(e) => p.navigate(p.makeHref("sources", { patch: { unknownOnly: e.target.checked } }))} /> Unknown only</label>
      </div>
      {rows.length === 0 ? <Empty>No sources in this range.</Empty> :
      <div className="card table-wrap"><table data-testid="sources-table"><thead><tr><Th k="name">Source</Th><Th k="messages">Messages</Th><th>Pass / forwarded / fail</th><Th k="spfAligned">SPF aligned</Th><Th k="dkimAligned">DKIM aligned</Th><th>DKIM domains</th><Th k="firstSeen">First seen</Th><Th k="lastSeen">Last seen</Th></tr></thead>
        <tbody>{rows.map((s) => <tr key={s.key} className={s.expected ? "" : "unknown-row"}><td><a href={p.makeHref("source", { key: s.key })}>{s.name || s.key}</a> {!s.expected && <span className="badge">unknown</span>}<div className="small muted">{s.kind} · {s.ips} IP(s)</div></td><td className="num">{formatNumber(s.messages)}</td><td><SplitBar pass={s.pass} forwarded={s.forwarded} fail={s.fail} /><div className="small num">{formatNumber(s.pass)} / {formatNumber(s.forwarded)} / {formatNumber(s.fail)}</div></td><td className="num">{formatPercent(s.spfAligned)}</td><td className="num">{formatPercent(s.dkimAligned)}</td><td>{s.dkimDomains.join(", ") || "—"}</td><td>{s.firstSeen}</td><td>{s.lastSeen}</td></tr>)}</tbody></table></div>}
    </div>
  );
}
