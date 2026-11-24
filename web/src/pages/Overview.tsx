import { useQuery } from "@tanstack/react-query";
import { api } from "../api/client.ts";
import type {
  NewSource,
  OverviewTotals,
  SourceVolume as SourceVolumeRow,
} from "../api/types.ts";
import type { PageProps } from "../page-props.ts";
import {
  Empty,
  ErrorBox,
  Loading,
  PageHeading,
  TableScroll,
} from "../components/ui.tsx";
import { policySummary, StackedBars } from "../components/DailyChart.tsx";
import { SplitBar } from "../components/Verdict.tsx";
import { StatCards } from "../components/StatCards.tsx";
import {
  formatNumber,
  formatPercent,
  formatUnixDateTime,
  formatDateLabel,
} from "../format.ts";

function Headline({ totals }: { totals: OverviewTotals }) {
  return (
    <StatCards
      stats={[
        {
          id: "messages",
          label: "Messages",
          value: formatNumber(totals.messages),
        },
        {
          id: "pass-rate",
          label: "Pass rate",
          value: formatPercent(totals.passRate),
          tone: "pass",
        },
        {
          id: "forwarded",
          label: "Forwarded",
          value: formatNumber(totals.forwarded),
          tone: "forwarded",
        },
        {
          id: "failing",
          label: "Failing",
          value: formatNumber(totals.fail),
          tone: "fail",
        },
        {
          id: "quarantined",
          label: "Quarantined",
          value: formatNumber(totals.quarantined),
        },
        {
          id: "rejected",
          label: "Rejected",
          value: formatNumber(totals.rejected),
        },
        {
          id: "reports",
          label: "Reports received",
          value: formatNumber(totals.reports),
        },
      ]}
    >
      <div className="card flex flex-col gap-2.5 py-2.5 sm:py-3.5">
        <dt className="text-xs font-medium text-ink-2 sm:text-sm">
          Verdict split
        </dt>
        <dd className="mt-1">
          <SplitBar {...totals} />
        </dd>
      </div>
    </StatCards>
  );
}

function TopSources({
  rows,
  makeHref,
}: {
  rows: SourceVolumeRow[];
  makeHref: PageProps["makeHref"];
}) {
  return (
    <section className="card">
      <h2>Top failing and forwarded sources</h2>
      {rows.length === 0 ? (
        <Empty>No failing or forwarded sources in this range.</Empty>
      ) : (
        <TableScroll label="Top failing and forwarded sources">
          <table
            className="text-xs sm:text-sm max-sm:[&_td]:px-2 max-sm:[&_th]:px-2"
            data-testid="top-failing-table"
          >
            <thead>
              <tr>
                <th scope="col">Source</th>
                <th scope="col" className="num">
                  Messages
                </th>
                <th scope="col" className="num">
                  Forwarded
                </th>
                <th scope="col" className="num">
                  Fail
                </th>
              </tr>
            </thead>
            <tbody>
              {rows.map((source) => (
                <tr
                  key={source.key}
                  className={source.expected ? "" : "unknown-row"}
                >
                  <td>
                    <a href={makeHref("source", { key: source.key })}>
                      {source.name || source.key}
                    </a>{" "}
                    {!source.expected && (
                      <span className="badge badge-unknown">Unknown</span>
                    )}
                  </td>
                  <td className="num">{formatNumber(source.messages)}</td>
                  <td className="num">{formatNumber(source.forwarded)}</td>
                  <td className="num">{formatNumber(source.fail)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </TableScroll>
      )}
    </section>
  );
}

function NewSources({
  rows,
  expected,
  makeHref,
}: {
  rows: NewSource[];
  expected: boolean;
  makeHref: PageProps["makeHref"];
}) {
  const kind = expected ? "expected" : "unknown";
  return (
    <section className="card" data-testid={`new-${kind}-sources`}>
      <h2>New {kind} sources</h2>
      {rows.length === 0 ? (
        <Empty>No new {kind} sources in this range.</Empty>
      ) : (
        <ul className="divide-y divide-line">
          {rows.map((source) => (
            <li
              key={source.key}
              className="space-y-1.5 py-2 first:pt-0 last:pb-0 [overflow-wrap:anywhere]"
            >
              <div className="flex flex-wrap items-baseline justify-between gap-x-2">
                <a href={makeHref("source", { key: source.key })}>
                  {source.name || source.key}
                </a>
                <span className="text-xs text-ink-2">
                  {formatNumber(source.messages)} messages
                </span>
              </div>
              <SplitBar {...source} />
              <p className="text-xs text-ink-2">
                {source.firstSeen} · {formatNumber(source.pass)} pass ·{" "}
                {formatNumber(source.forwarded)} forwarded ·{" "}
                {formatNumber(source.fail)} fail
              </p>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

export default function Overview({ eff, makeHref, tz }: PageProps) {
  const query = useQuery({
    queryKey: ["overview", eff],
    queryFn: () => api.overview(eff),
  });
  const data = query.data;
  return (
    <div className="page">
      <PageHeading testid="overview-heading">Overview</PageHeading>
      {!data ? (
        query.isError ? (
          <ErrorBox error={query.error} />
        ) : (
          <Loading />
        )
      ) : (
        <>
          <Headline totals={data.totals} />
          <section className="card">
            <h2>Daily messages by verdict</h2>
            <StackedBars days={data.days} policyChanges={data.policyChanges} />
            {data.policyChanges.length > 0 && (
              <details
                className="mt-2 border-t border-line pt-2"
                data-testid="policy-changes"
              >
                <summary className="w-fit font-medium text-accent">
                  Policy changes ({formatNumber(data.policyChanges.length)})
                </summary>
                <ul className="mt-2 space-y-1.5 text-xs">
                  {data.policyChanges.map((change) => (
                    <li
                      key={change.reportId}
                      className="[overflow-wrap:anywhere]"
                    >
                      <a
                        href={makeHref("report", {
                          id: String(change.reportId),
                        })}
                      >
                        {formatDateLabel(change.date)} · {change.domain}
                      </a>
                      <span className="text-ink-2">
                        {" "}
                        · {policySummary(change)}
                      </span>
                    </li>
                  ))}
                </ul>
              </details>
            )}
          </section>
          {data.longWindows > 0 && (
            <p data-testid="long-windows-note" className="text-xs text-ink-2">
              {formatNumber(data.longWindows)} reports cover more than 48 hours.
              Their messages count on the day of the window midpoint.
            </p>
          )}
          <div className="grid min-w-0 items-start gap-4 lg:grid-cols-[1.6fr_1fr]">
            <TopSources rows={data.topFailing} makeHref={makeHref} />
            <div className="grid min-w-0 gap-4">
              <NewSources
                rows={data.newUnknown}
                expected={false}
                makeHref={makeHref}
              />
              <NewSources
                rows={data.newExpected}
                expected
                makeHref={makeHref}
              />
              <section className="card">
                <h2>Quiet reporters</h2>
                {data.quietReporters.length === 0 ? (
                  <Empty>No quiet reporters.</Empty>
                ) : (
                  <ul className="divide-y divide-line">
                    {data.quietReporters.map((reporter) => (
                      <li
                        key={reporter.org}
                        className="space-y-0.5 py-2 first:pt-0 last:pb-0 [overflow-wrap:anywhere]"
                      >
                        <p className="font-medium">{reporter.org}</p>
                        <p className="text-xs text-ink-2">
                          Last report{" "}
                          {formatUnixDateTime(reporter.lastReportAt, tz)} ·{" "}
                          {reporter.domains.join(", ")}
                        </p>
                      </li>
                    ))}
                  </ul>
                )}
              </section>
            </div>
          </div>
        </>
      )}
    </div>
  );
}
