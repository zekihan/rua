import type { TLSPolicy } from "../../api/types.ts";
import { Empty, TableScroll } from "../../components/ui.tsx";
import { formatNumber } from "../../format.ts";

export function ReportTlsPolicies({ policies }: { policies: TLSPolicy[] }) {
  return (
    <section className="card">
      <h2>TLS policies</h2>
      {policies.length === 0 ? (
        <Empty>No TLS policies.</Empty>
      ) : (
        <TableScroll label="Report TLS policies">
          <table
            className="min-w-[40rem]"
            data-testid="report-tls-policies-table"
          >
            <thead>
              <tr>
                <th>Type / domain</th>
                <th>Policy strings</th>
                <th>MX hosts</th>
                <th className="num">Success / failure</th>
                <th>Failure details</th>
              </tr>
            </thead>
            <tbody>
              {policies.map((pol, i) => (
                <tr key={i}>
                  <td>
                    {pol.type} · {pol.domain}
                  </td>
                  <td className="mono small">{pol.strings.join(" ") || "—"}</td>
                  <td className="mono small">
                    {pol.mxHosts.join(", ") || "—"}
                  </td>
                  <td className="num">
                    {formatNumber(pol.success)} / {formatNumber(pol.failure)}
                  </td>
                  <td>
                    {pol.failures.length
                      ? pol.failures.map((f, j) => (
                          <div key={j} className="small">
                            {f.resultType}: {formatNumber(f.failedSessions)}{" "}
                            failed ({f.sendingIp || "—"} →{" "}
                            {f.receivingMx || f.receivingIp || "—"}
                            {f.reasonCode ? `, ${f.reasonCode}` : ""})
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
