import { useQuery } from "@tanstack/react-query";
import { api } from "../api/client.ts";
import type { PageProps } from "../page-props.ts";
import { Loading, ErrorBox, Empty, PageHeading } from "../components/ui.tsx";
import { formatNumber, formatUnixDateTime } from "../format.ts";
import {
  Mailboxes,
  MessageCounts,
  Events,
  FailureReports,
} from "./status/StatusTables.tsx";
import { FetchPanel } from "./status/FetchPanel.tsx";
import { MessageTable } from "./status/MessageTable.tsx";

export default function StatusPage(p: PageProps) {
  const query = useQuery({
    queryKey: ["status"],
    queryFn: api.status,
    refetchInterval: 30_000,
  });
  const d = query.data;
  return (
    <div className="page">
      <PageHeading testid="status-heading">Status</PageHeading>
      {!d ? (
        query.isError ? (
          <ErrorBox error={query.error} />
        ) : (
          <Loading />
        )
      ) : (
        <>
          {query.isError && <ErrorBox error={query.error} />}
          {d.stale && (
            <p
              role="alert"
              data-testid="stale-banner"
              className="card border-fail/40 text-fail"
            >
              <strong>Stale:</strong> no successful fetch within{" "}
              {formatNumber(d.staleAfterSeconds)} seconds. Last success{" "}
              {formatUnixDateTime(d.lastSuccessAt, p.tz)}.
            </p>
          )}
          <FetchPanel status={d} tz={p.tz} />
          <Mailboxes rows={d.mailboxes} tz={p.tz} />
          <div className="grid min-w-0 items-start gap-4 lg:grid-cols-[1fr_1.45fr]">
            <section className="card">
              <h2>Conflicting reports</h2>
              {d.conflicts.length === 0 ? (
                <Empty>No conflicts.</Empty>
              ) : (
                <ul className="space-y-1 [overflow-wrap:anywhere]">
                  {d.conflicts.map((c) => (
                    <li key={c.id}>
                      <a href={p.makeHref("report", { id: String(c.id) })}>
                        #{c.id} {c.org} — {c.domain}
                      </a>{" "}
                      <span className="badge badge-fail">conflict</span>
                    </li>
                  ))}
                </ul>
              )}
            </section>
            <MessageCounts rows={d.classes} />
          </div>
          <MessageTable
            title="Problem messages"
            rows={d.problems}
            testid="problems-table"
            tz={p.tz}
          />
          <MessageTable
            title="Other messages"
            rows={d.other}
            testid="other-messages-table"
            tz={p.tz}
          />
          <FailureReports rows={d.failureReports} tz={p.tz} />
          <Events rows={d.events} tz={p.tz} />
        </>
      )}
    </div>
  );
}
