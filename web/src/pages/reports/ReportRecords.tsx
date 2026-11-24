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
        <TableScroll label="Report records">
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
                      from {rec.envelopeFrom || "—"} → {rec.envelopeTo || "—"};
                      header-from {rec.headerFrom || "—"}
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
      )}
    </section>
  );
}
