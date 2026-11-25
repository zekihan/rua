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
import { TLSBars, TLSFailureBars } from "../components/DailyChart.tsx";
import { formatNumber, formatUnixDateTime } from "../format.ts";

export function TlsList(p: PageProps) {
  const q = useQuery({
    queryKey: ["tls", p.eff],
    queryFn: () => api.tls(p.eff),
  });
  if (q.isLoading) return <Loading />;
  if (q.error) return <ErrorBox error={q.error} />;
  const rows = q.data!.domains;
  return (
    <div className="page">
      <PageHeading testid="tls-heading">TLS reports</PageHeading>
      {rows.length === 0 ? (
        <Empty>No TLS reports in this range.</Empty>
      ) : (
        <TableScroll label="TLS domains">
          <table
            className="min-w-[34rem] bg-surface"
            data-testid="tls-domains-table"
          >
            <thead>
              <tr>
                <th>Domain</th>
                <th className="num">Success</th>
                <th className="num">Failure</th>
                <th className="num">Reports</th>
                <th>Last report</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((d) => (
                <tr key={d.domain}>
                  <td>
                    <a href={p.makeHref("tlsDomain", { key: d.domain })}>
                      {d.domain}
                    </a>
                  </td>
                  <td className="num">{formatNumber(d.success)}</td>
                  <td className="num">{formatNumber(d.failure)}</td>
                  <td className="num">{formatNumber(d.reports)}</td>
                  <td>{formatUnixDateTime(d.lastReportAt, p.tz)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </TableScroll>
      )}
    </div>
  );
}

export function TlsDomain(p: PageProps) {
  const domain = p.state.key;
  const q = useQuery({
    queryKey: ["tlsDomain", domain, p.eff],
    queryFn: () => api.tlsDomain(domain, p.eff),
  });
  if (q.isLoading) return <Loading />;
  if (q.error) return <ErrorBox error={q.error} />;
  const d = q.data!;
  return (
    <div className="page">
      <a className="link w-fit text-xs" href={p.makeHref("tls")}>
        ← TLS reports
      </a>
      <PageHeading testid="tls-domain-heading">{d.domain}</PageHeading>
      <p className="muted [overflow-wrap:anywhere]">
        Successful sessions {formatNumber(d.success)} · failed{" "}
        {formatNumber(d.failure)}
      </p>
      <section className="card">
        <h2>Daily sessions</h2>
        <TLSBars days={d.days} />
      </section>
      <section className="card">
        <h2>Failed sessions per day</h2>
        <TLSFailureBars days={d.days} />
      </section>
      <section className="card">
        <h2>Failure types</h2>
        {d.failures.length === 0 ? (
          <Empty>
            {d.failure > 0
              ? "No failure details reported."
              : "No failures in this range."}
          </Empty>
        ) : (
          <TableScroll label="TLS failure types">
            <table className="min-w-[40rem]" data-testid="tls-failures-table">
              <thead>
                <tr>
                  <th>Result type</th>
                  <th className="num">Sessions</th>
                  <th>MX hosts</th>
                  <th>Receiving IPs</th>
                  <th>Reason codes</th>
                </tr>
              </thead>
              <tbody>
                {d.failures.map((f) => (
                  <tr key={f.resultType}>
                    <td className="mono">{f.resultType}</td>
                    <td className="num">{formatNumber(f.sessions)}</td>
                    <td className="mono small">
                      {f.mxHosts.join(", ") || "—"}
                    </td>
                    <td className="mono small">
                      {f.receivingIps.join(", ") || "—"}
                    </td>
                    <td className="mono small">
                      {f.reasonCodes.join(", ") || "—"}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </TableScroll>
        )}
      </section>
      <section className="card">
        <h2>Policies</h2>
        {d.policies.length === 0 ? (
          <Empty>No policies in this range.</Empty>
        ) : (
          <TableScroll label="TLS policies">
            <table className="min-w-[43rem]" data-testid="tls-policies-table">
              <thead>
                <tr>
                  <th>Type</th>
                  <th>Domain</th>
                  <th>Policy strings</th>
                  <th>MX hosts</th>
                  <th className="num">Success / failure</th>
                  <th className="num">Reports</th>
                </tr>
              </thead>
              <tbody>
                {d.policies.map((pol, i) => (
                  <tr key={i}>
                    <td>{pol.type}</td>
                    <td className="whitespace-nowrap">{pol.domain}</td>
                    <td className="mono small">
                      {pol.strings.join(" ") || "—"}
                    </td>
                    <td className="mono small">
                      {pol.mxHosts.join(", ") || "—"}
                    </td>
                    <td className="num">
                      {formatNumber(pol.success)} / {formatNumber(pol.failure)}
                    </td>
                    <td className="num">{formatNumber(pol.reports)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </TableScroll>
        )}
      </section>
      <section className="card">
        <h2>Reporters</h2>
        {d.reporters.length === 0 ? (
          <Empty>No reporters.</Empty>
        ) : (
          <ul className="space-y-1 [overflow-wrap:anywhere]">
            {d.reporters.map((r, i) => (
              <li key={i}>
                {r.value}{" "}
                <span className="muted small">
                  {formatNumber(r.messages)} sessions
                </span>
              </li>
            ))}
          </ul>
        )}
      </section>
    </div>
  );
}
