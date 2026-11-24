import { useInfiniteQuery } from "@tanstack/react-query";
import { api } from "../api/client.ts";
import type { ReportSummary } from "../api/types.ts";
import type { PageProps } from "../page-props.ts";
import type { Filters } from "../url.ts";
import { ReportMarkers } from "../components/ReportMarkers.tsx";
import {
  Loading,
  ErrorBox,
  Empty,
  Field,
  PageHeading,
  TableScroll,
} from "../components/ui.tsx";
import { formatNumber, formatPercent, formatUnixDateTime } from "../format.ts";

function ReportFilters({
  filters,
  update,
}: {
  filters: Filters;
  update: (patch: Partial<Filters>) => void;
}) {
  return (
    <div className="flex flex-wrap items-end gap-3">
      <Field label="Reporter">
        <input
          key={filters.reporter}
          className="input sm:w-52"
          data-testid="reports-reporter-filter"
          defaultValue={filters.reporter}
          placeholder="Exact reporter name"
          onBlur={(event) =>
            update({ reporter: event.currentTarget.value.trim() })
          }
          onKeyDown={(event) => {
            if (event.key === "Enter") event.currentTarget.blur();
          }}
        />
      </Field>
      <Field label="Kind">
        <select
          className="input"
          data-testid="reports-kind-filter"
          value={filters.kind}
          onChange={(event) => update({ kind: event.target.value })}
        >
          <option value="">All kinds</option>
          <option value="dmarc-aggregate">DMARC aggregate</option>
          <option value="tls-report">TLS report</option>
        </select>
      </Field>
      <Field label="Verdict">
        <select
          className="input opacity-65"
          data-testid="reports-verdict-filter"
          disabled
          defaultValue=""
        >
          <option value="">Unavailable for reports</option>
        </select>
      </Field>
    </div>
  );
}

function ReportTable({
  rows,
  tz,
  makeHref,
}: {
  rows: ReportSummary[];
  tz: string;
  makeHref: PageProps["makeHref"];
}) {
  return (
    <TableScroll label="Reports">
      <table className="min-w-[49rem] bg-surface" data-testid="reports-table">
        <thead>
          <tr>
            <th scope="col">Reporter</th>
            <th scope="col">Domain</th>
            <th scope="col">Window</th>
            <th scope="col" className="num">
              Messages
            </th>
            <th scope="col" className="num">
              Pass rate / TLS failed
            </th>
            <th scope="col">Markers</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((report) => {
            const tls = report.kind === "tls-report";
            return (
              <tr key={report.id}>
                <td>
                  <a href={makeHref("report", { id: String(report.id) })}>
                    {report.org || report.reportId || `#${report.id}`}
                  </a>
                  <p className="small muted">
                    {tls ? "TLS" : "DMARC"} · {report.date}
                  </p>
                </td>
                <td>{report.domain || "—"}</td>
                <td className="text-xs tabular-nums">
                  {formatUnixDateTime(report.begin, tz)}{" "}
                  <span className="text-ink-2">to</span>{" "}
                  {formatUnixDateTime(report.end, tz)}
                </td>
                <td className="num">
                  {tls ? "—" : formatNumber(report.messages)}
                </td>
                <td className="num">
                  {tls
                    ? `${formatNumber(report.tlsFailed)} failed`
                    : formatPercent(report.passRate)}
                </td>
                <td>
                  <ReportMarkers report={report} />
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </TableScroll>
  );
}

export default function Reports({
  state,
  eff,
  makeHref,
  navigate,
  tz,
}: PageProps) {
  const { domain, from, to, reporter, kind, verdict, source } = eff;
  const filters = { domain, from, to, reporter, kind, verdict, source, tz };
  const query = useInfiniteQuery({
    queryKey: ["reports", filters],
    queryFn: ({ pageParam }) =>
      api.reports({ ...filters, cursor: pageParam, limit: 50 }),
    initialPageParam: "",
    getNextPageParam: (page) => page.nextCursor || undefined,
  });
  const rows = query.data?.pages.flatMap((page) => page.reports);
  const update = (patch: Partial<Filters>) =>
    navigate(makeHref("reports", { patch }));

  return (
    <div className="page">
      <PageHeading testid="reports-heading">Reports</PageHeading>
      <ReportFilters filters={state.filters} update={update} />
      {!rows ? (
        query.isError ? (
          <ErrorBox error={query.error} />
        ) : (
          <Loading />
        )
      ) : rows.length === 0 ? (
        <Empty>No reports match these filters.</Empty>
      ) : (
        <>
          <ReportTable rows={rows} tz={tz} makeHref={makeHref} />
          {query.isError && <ErrorBox error={query.error} />}
          {query.hasNextPage && (
            <button
              type="button"
              className="btn justify-self-start"
              data-testid="reports-load-more"
              disabled={query.isFetchingNextPage}
              onClick={() => void query.fetchNextPage()}
            >
              {query.isFetchingNextPage ? "Loading…" : "Load more"}
            </button>
          )}
        </>
      )}
    </div>
  );
}
