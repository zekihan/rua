import type { RecordDetail } from "../../api/types.ts";
import type { PageProps } from "../../page-props.ts";
import { VerdictBadge } from "../../components/Verdict.tsx";
import { Empty, TableScroll } from "../../components/ui.tsx";
import { formatNumber } from "../../format.ts";

export function ReportRecords({
  records,
  makeHref,
}: {
  records: RecordDetail[];
  makeHref: PageProps["makeHref"];
}) {
  return (
    <section className="card">
      <h2>Records</h2>
      {records.length === 0 ? (
        <Empty>No records.</Empty>
      ) : (
        <>
          <ul
            className="grid gap-2 sm:hidden"
            data-testid="report-records-cards"
          >
            {records.map((rec, i) => (
              <li
                key={i}
                data-testid="report-record-card"
                className="space-y-1.5 rounded-lg border border-line bg-surface p-3"
              >
                <div className="flex flex-wrap items-baseline justify-between gap-x-2">
                  <span className="mono font-semibold">{rec.sourceIp}</span>
                  <span className="tabular-nums">
                    <strong>{formatNumber(rec.count)}</strong>{" "}
                    <span className="small muted">messages</span>
                  </span>
                </div>
                <p className="small">
                  <a href={makeHref("source", { key: rec.sourceKey })}>
                    {rec.sourceName || rec.sourceKey}
                  </a>
                </p>
                <p className="flex flex-wrap items-center gap-x-2 gap-y-1">
                  <VerdictBadge verdict={rec.verdict} />
                  <span className="small">
                    <span className="muted">Disposition</span>{" "}
                    {rec.disposition || "—"}
                  </span>
                </p>
                <p className="small">
                  SPF {rec.spf} · DKIM {rec.dkim}
                </p>
                {rec.reasons.map((x, j) => (
                  <p key={j} className="small muted">
                    {x.type}
                    {x.comment ? `: ${x.comment}` : ""}
                  </p>
                ))}
                <details className="small" data-testid="record-auth-details">
                  <summary className="w-fit font-medium text-accent">
                    Auth results ({rec.auth.length})
                  </summary>
                  <div className="mt-1 space-y-1">
                    {rec.auth.length
                      ? rec.auth.map((a, j) => (
                          <div key={j} className="mono">
                            {a.method}: {a.domain || "—"}
                            {a.selector ? ` (${a.selector})` : ""}
                            {a.scope ? ` [${a.scope}]` : ""} = {a.result}
                          </div>
                        ))
                      : "—"}
                    <div className="muted">
                      from {rec.envelopeFrom || "—"} → {rec.envelopeTo || "—"};
                      header-from {rec.headerFrom || "—"}
                    </div>
                  </div>
                </details>
              </li>
            ))}
          </ul>
          <TableScroll label="Report records" className="max-sm:hidden">
            <table className="min-w-[45rem]" data-testid="report-records-table">
              <thead>
                <tr>
                  <th>Source</th>
                  <th className="num">Count</th>
                  <th>Disposition</th>
                  <th>SPF / DKIM</th>
                  <th>Verdict</th>
                  <th>Auth results</th>
                </tr>
              </thead>
              <tbody>
                {records.map((rec, i) => (
                  <tr key={i}>
                    <td className="mono">
                      {rec.sourceIp}
                      <div className="small">
                        <a href={makeHref("source", { key: rec.sourceKey })}>
                          {rec.sourceName || rec.sourceKey}
                        </a>
                      </div>
                      <div className="small muted">
                        from {rec.envelopeFrom || "—"} → {rec.envelopeTo || "—"}
                        ; header-from {rec.headerFrom || "—"}
                      </div>
                    </td>
                    <td className="num">{formatNumber(rec.count)}</td>
                    <td>{rec.disposition}</td>
                    <td>
                      SPF {rec.spf} · DKIM {rec.dkim}
                    </td>
                    <td>
                      <VerdictBadge verdict={rec.verdict} />
                      {rec.reasons.map((x, j) => (
                        <div key={j} className="small muted">
                          {x.type}
                          {x.comment ? `: ${x.comment}` : ""}
                        </div>
                      ))}
                    </td>
                    <td>
                      {rec.auth.length
                        ? rec.auth.map((a, j) => (
                            <div key={j} className="small mono">
                              {a.method}: {a.domain || "—"}
                              {a.selector ? ` (${a.selector})` : ""}
                              {a.scope ? ` [${a.scope}]` : ""} = {a.result}
                            </div>
                          ))
                        : "—"}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </TableScroll>
        </>
      )}
    </section>
  );
}
