import { useQuery } from "@tanstack/react-query";
import { api, reportRawHref, messageRawHref } from "../api/client.ts";
import type { PageProps } from "../page-props.ts";
import { Loading, ErrorBox, Empty, PageHeading } from "../components/ui.tsx";
import { ReportMarkers } from "../components/ReportMarkers.tsx";
import { ReportRecords } from "./reports/ReportRecords.tsx";
import { ReportTlsPolicies } from "./reports/ReportTlsPolicies.tsx";
import { formatPercent, formatUnixDateTime } from "../format.ts";

export default function ReportDetail(p: PageProps) {
  const id = p.state.id;
  const q = useQuery({
    queryKey: ["report", id, p.tz],
    queryFn: () => api.report(id, p.tz),
  });
  if (q.isLoading) return <Loading />;
  if (q.error) return <ErrorBox error={q.error} />;
  const d = q.data!;
  const r = d.report;
  const { policy } = d;
  const isTls = r.kind === "tls-report";
  return (
    <div className="page">
      <a className="link w-fit text-xs" href={p.makeHref("reports")}>
        ← Reports
      </a>
      <PageHeading testid="report-detail-heading">Report #{r.id}</PageHeading>
      <p className="muted [overflow-wrap:anywhere]">
        {r.kind} · {r.org} · {r.domain} · report ID{" "}
        <span className="mono">{r.reportId || "—"}</span> ·{" "}
        {formatUnixDateTime(r.begin, p.tz)} – {formatUnixDateTime(r.end, p.tz)}{" "}
        <ReportMarkers report={r} />
      </p>
      <div className="flex flex-wrap gap-2.5">
        <a
          className="btn"
          data-testid="report-raw-download"
          href={reportRawHref(r.id)}
          download
        >
          Download extracted report
        </a>
        {d.messageId > 0 && (
          <a
            className="btn btn-secondary"
            data-testid="message-raw-download"
            href={messageRawHref(d.messageId)}
            download
          >
            Download stored message
          </a>
        )}
      </div>
      {!isTls && (
        <section className="card">
          <h2>Published policy</h2>
          {policy ? (
            <dl className="mb-3 grid grid-cols-3 gap-2 sm:grid-cols-5 lg:grid-cols-9">
              {(
                [
                  "p",
                  "sp",
                  "np",
                  "pct",
                  "adkim",
                  "aspf",
                  "fo",
                  "testing",
                  "discovery",
                ] as const
              ).map((k) => (
                <div key={k}>
                  <dt className="small muted">{k}</dt>
                  <dd className="mono">
                    {(k === "pct" ? policy.pct : policy[k] || null) ?? "—"}
                  </dd>
                </div>
              ))}
            </dl>
          ) : (
            <Empty>No published policy in this report.</Empty>
          )}
          <p className="mt-3 text-xs text-ink-2 [overflow-wrap:anywhere]">
            Reporter email: {d.email || "—"}
            {d.extraContact ? ` · extra: ${d.extraContact}` : ""}
            {d.schema ? ` · schema: ${d.schema}` : ""}
          </p>
        </section>
      )}
      {isTls ? (
        <ReportTlsPolicies policies={d.tlsPolicies} />
      ) : (
        <ReportRecords records={d.records} makeHref={p.makeHref} />
      )}
      <section className="card">
        <h2>Copies</h2>
        {d.copies.length === 0 ? (
          <Empty>No other copies.</Empty>
        ) : (
          <ul className="space-y-1 [overflow-wrap:anywhere]">
            {d.copies.map((c) => (
              <li key={c.id}>
                <a href={p.makeHref("report", { id: String(c.id) })}>
                  #{c.id} {c.org}
                </a>{" "}
                <span className="small muted">
                  {c.passRate !== null && `${formatPercent(c.passRate)} pass `}
                  <ReportMarkers report={c} />
                </span>
              </li>
            ))}
          </ul>
        )}
      </section>
    </div>
  );
}
