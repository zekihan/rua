import { useQuery } from "@tanstack/react-query";
import { api } from "../api/client.ts";
import type { PageProps } from "../page-props.ts";
import {
  Loading,
  ErrorBox,
  Empty,
  PageHeading,
  TableScroll,
} from "../components/ui.tsx";
import { VerdictBadge } from "../components/Verdict.tsx";
import { StackedBars } from "../components/DailyChart.tsx";
import { SplitBar } from "../components/Verdict.tsx";
import { formatNumber } from "../format.ts";
import type { Count } from "../api/types.ts";

function CountList({
  title,
  items,
  testid,
}: {
  title: string;
  items: Count[];
  testid: string;
}) {
  return (
    <section className="card">
      <h2>{title}</h2>
      {items.length === 0 ? (
        <Empty>None in this range.</Empty>
      ) : (
        <TableScroll label={title}>
          <table data-testid={testid}>
            <tbody>
              {items.map((c, i) => (
                <tr key={i}>
                  <td className="mono">{c.value || "—"}</td>
                  <td className="num">{formatNumber(c.messages)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </TableScroll>
      )}
    </section>
  );
}
export default function SourceDetail(p: PageProps) {
  const key = p.state.key;
  const q = useQuery({
    queryKey: ["source", key, p.eff],
    queryFn: () => api.source(key, p.eff),
  });
  if (q.isLoading) return <Loading />;
  if (q.error) return <ErrorBox error={q.error} />;
  const d = q.data!;
  const s = d.source;
  return (
    <div className="page">
      <a className="link w-fit text-xs" href={p.makeHref("sources")}>
        ← Sources
      </a>
      <PageHeading testid="source-detail-heading">{s.name || key}</PageHeading>
      <p className="muted [overflow-wrap:anywhere]">
        {s.kind} · {s.expected ? "Expected source" : "Unknown source"}{" "}
        {s.rule ? `· Matching rule: ${s.rule}` : "· No matching rule"} ·{" "}
        {formatNumber(s.messages)} messages · first seen {s.firstSeen}, last
        seen {s.lastSeen}
      </p>
      <section className="card">
        <h2>Daily volume</h2>
        <StackedBars days={d.days} />
      </section>
      <section className="card">
        <h2>IP addresses</h2>
        {d.ips.length === 0 ? (
          <Empty>No IPs in this range.</Empty>
        ) : (
          <TableScroll label="IP addresses">
            <table className="min-w-[35rem]" data-testid="source-ips-table">
              <thead>
                <tr>
                  <th>IP</th>
                  <th>PTR</th>
                  <th>ASN / org</th>
                  <th className="num">Messages</th>
                  <th>Split</th>
                </tr>
              </thead>
              <tbody>
                {d.ips.map((ip) => (
                  <tr key={ip.ip}>
                    <td className="mono">{ip.ip}</td>
                    <td className="mono">
                      {ip.ptr || "—"}{" "}
                      {ip.ptrConfirmed ? (
                        <span className="badge" title="Forward-confirmed PTR">
                          ✓ confirmed
                        </span>
                      ) : null}
                    </td>
                    <td>
                      {ip.asn ? `AS${ip.asn} ` : ""}
                      {ip.asOrg || "—"}
                    </td>
                    <td className="num">{formatNumber(ip.messages)}</td>
                    <td>
                      <SplitBar
                        pass={ip.pass}
                        forwarded={ip.forwarded}
                        fail={ip.fail}
                      />
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </TableScroll>
        )}
      </section>
      <section className="card">
        <h2>Result combinations</h2>
        {d.combinations.length === 0 ? (
          <Empty>No records in this range.</Empty>
        ) : (
          <TableScroll label="Result combinations">
            <table data-testid="combinations-table">
              <thead>
                <tr>
                  <th>SPF</th>
                  <th>DKIM</th>
                  <th>SPF aligned</th>
                  <th>DKIM aligned</th>
                  <th>Disposition</th>
                  <th>Verdict</th>
                  <th className="num">Messages</th>
                </tr>
              </thead>
              <tbody>
                {d.combinations.map((c, i) => (
                  <tr key={i}>
                    <td>{c.spf}</td>
                    <td>{c.dkim}</td>
                    <td>{c.spfAligned ? "yes" : "no"}</td>
                    <td>{c.dkimAligned ? "yes" : "no"}</td>
                    <td>{c.disposition}</td>
                    <td>
                      <VerdictBadge verdict={c.verdict} />
                    </td>
                    <td className="num">{formatNumber(c.messages)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </TableScroll>
        )}
      </section>
      <div className="grid min-w-0 gap-4 md:grid-cols-2">
        <CountList
          title="Envelope-from"
          items={d.envelopeFrom}
          testid="envelope-from-table"
        />
        <CountList
          title="Header-from"
          items={d.headerFrom}
          testid="header-from-table"
        />
        <CountList
          title="DKIM signatures"
          items={d.dkimSignatures}
          testid="dkim-signatures-table"
        />
        <CountList
          title="Reporters"
          items={d.reporters}
          testid="source-reporters-table"
        />
      </div>
    </div>
  );
}
