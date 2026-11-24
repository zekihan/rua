import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { api } from "../../api/client.ts";
import type { PageProps } from "../../page-props.ts";
import { collapseRows, RECEIVERS_COLLAPSED } from "../../receivers.ts";
import { formatNumber, formatUnixDateTime } from "../../format.ts";
import { Empty, Loading } from "../../components/ui.tsx";

export function Receivers({
  eff,
  makeHref,
  reporter,
  tz,
}: Pick<PageProps, "eff" | "makeHref" | "tz"> & { reporter: string }) {
  const [expanded, setExpanded] = useState(false);
  const { domain, from, to } = eff;
  const query = useQuery({
    queryKey: ["reporters", domain, from, to, tz],
    queryFn: () => api.reporters({ domain, from, to, tz }),
    retry: false,
  });
  const rows = query.data?.reporters;
  const { shown, hidden } = collapseRows(rows ?? [], expanded);

  return (
    <section className="card" data-testid="receivers">
      <h2>Receivers</h2>
      {query.isError ? (
        <p className="text-sm text-ink-2" data-testid="receivers-error">
          Receivers are unavailable right now.
        </p>
      ) : !rows ? (
        <Loading />
      ) : rows.length === 0 ? (
        <Empty>No receivers reported in this range.</Empty>
      ) : (
        <>
          <ul className="divide-y divide-line" data-testid="receivers-list">
            {shown.map((r) => (
              <li
                key={r.org}
                data-testid="receiver-row"
                className="flex flex-wrap items-baseline gap-x-3 gap-y-0.5 py-2 first:pt-0 last:pb-0"
              >
                <a
                  className="font-semibold [overflow-wrap:anywhere]"
                  href={makeHref("reports", { patch: { reporter: r.org } })}
                  aria-current={reporter === r.org ? "true" : undefined}
                  title={`Show reports from ${r.org}`}
                >
                  {r.org}
                </a>
                {r.quiet && (
                  <span
                    className="badge badge-fail"
                    data-testid="receiver-quiet"
                    title="Reported before, but nothing for 7 days"
                  >
                    Quiet
                  </span>
                )}
                <span className="small muted tabular-nums">
                  {formatNumber(r.reports)}{" "}
                  {r.reports === 1 ? "report" : "reports"} ·{" "}
                  {formatNumber(r.messages)} messages · last{" "}
                  {formatUnixDateTime(r.lastReportAt, tz)}
                </span>
                <span className="small muted basis-full [overflow-wrap:anywhere]">
                  {r.domains.join(", ") || "—"}
                </span>
              </li>
            ))}
          </ul>
          {rows.length > RECEIVERS_COLLAPSED && (
            <button
              type="button"
              className="mt-3 text-xs font-medium text-accent hover:underline"
              data-testid="receivers-toggle"
              aria-expanded={expanded}
              onClick={() => setExpanded(!expanded)}
            >
              {expanded
                ? "Show fewer"
                : `Show all ${rows.length} receivers (${hidden} more)`}
            </button>
          )}
        </>
      )}
    </section>
  );
}
