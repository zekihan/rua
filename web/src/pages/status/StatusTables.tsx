import { messageRawHref } from "../../api/client.ts";
import type {
  MailboxStatus,
  ClassCount,
  Event,
  FailureReport,
} from "../../api/types.ts";
import { Empty, TableScroll } from "../../components/ui.tsx";
import {
  formatNumber,
  formatPending,
  formatUnixDateTime,
} from "../../format.ts";

export function Mailboxes({ rows, tz }: { rows: MailboxStatus[]; tz: string }) {
  return (
    <section className="card">
      <h2>Mailboxes</h2>
      {rows.length === 0 ? (
        <Empty>No mailboxes configured.</Empty>
      ) : (
        <>
          <ul className="grid gap-2 sm:hidden" data-testid="mailboxes-cards">
            {rows.map((m) => (
              <li
                key={m.name}
                data-testid="mailbox-card"
                className="space-y-1 rounded-lg border border-line bg-surface p-3"
              >
                <p className="flex flex-wrap items-center gap-x-2 gap-y-1 font-semibold">
                  {m.name}
                  {!m.configured && (
                    <span className="badge">not configured (history)</span>
                  )}
                </p>
                <p className="small" data-testid="mailbox-card-pending">
                  <span className="muted">Pending</span>{" "}
                  {m.pending !== null && m.pending > 0 ? (
                    <span className="badge badge-unknown">
                      {formatPending(m.pending)}
                    </span>
                  ) : (
                    <strong>{formatPending(m.pending)}</strong>
                  )}
                </p>
                <p className="small muted tabular-nums">
                  UIDVALIDITY {m.uidValidity} · next UID {m.nextUid}
                </p>
                <p className="small">
                  {m.lastResult || "—"} ·{" "}
                  {formatUnixDateTime(m.lastFetchEndAt, tz)}
                </p>
                {m.lastError && (
                  <p className="small text-fail [overflow-wrap:anywhere]">
                    {m.lastError}
                  </p>
                )}
              </li>
            ))}
          </ul>
          <TableScroll label="Mailbox checkpoints" className="max-sm:hidden">
            <table className="min-w-[52rem]" data-testid="mailboxes-table">
              <thead>
                <tr>
                  <th>Name</th>
                  <th className="num">UIDVALIDITY</th>
                  <th className="num">Next UID</th>
                  <th className="num">Pending</th>
                  <th>Last fetch</th>
                  <th>Last success</th>
                  <th>Result</th>
                </tr>
              </thead>
              <tbody>
                {rows.map((m) => (
                  <tr key={m.name}>
                    <td>
                      {m.name}
                      {!m.configured && (
                        <div>
                          <span
                            className="badge"
                            data-testid="mailbox-not-configured"
                          >
                            not configured (history)
                          </span>
                        </div>
                      )}
                    </td>
                    <td className="num">{m.uidValidity}</td>
                    <td className="num">{m.nextUid}</td>
                    <td className="num" data-testid="mailbox-pending">
                      {m.pending !== null && m.pending > 0 ? (
                        <span className="badge badge-unknown">
                          {formatPending(m.pending)}
                        </span>
                      ) : (
                        formatPending(m.pending)
                      )}
                    </td>
                    <td className="small tabular-nums">
                      {formatUnixDateTime(m.lastFetchStartAt, tz)} –{" "}
                      {formatUnixDateTime(m.lastFetchEndAt, tz)}
                    </td>
                    <td className="small tabular-nums">
                      {formatUnixDateTime(m.lastSuccessAt, tz)}
                    </td>
                    <td>
                      {m.lastResult || "—"}
                      {m.lastError && (
                        <div className="small muted">{m.lastError}</div>
                      )}
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

export function MessageCounts({ rows }: { rows: ClassCount[] }) {
  return (
    <section className="card">
      <h2>Message counts</h2>
      {rows.length === 0 ? (
        <Empty>No messages yet.</Empty>
      ) : (
        <TableScroll label="Message counts">
          <table data-testid="classes-table">
            <thead>
              <tr>
                <th>Class</th>
                <th>Status</th>
                <th className="num">Messages</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((c, i) => (
                <tr key={i}>
                  <td>{c.class}</td>
                  <td>{c.status}</td>
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

export function Events({ rows, tz }: { rows: Event[]; tz: string }) {
  return (
    <section className="card">
      <h2>Events</h2>
      {rows.length === 0 ? (
        <Empty>No events.</Empty>
      ) : (
        <TableScroll label="Events">
          <table className="min-w-[30rem]" data-testid="events-table">
            <thead>
              <tr>
                <th>At</th>
                <th>Kind</th>
                <th>Mailbox</th>
                <th>Detail</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((e, i) => (
                <tr key={i}>
                  <td className="small">{formatUnixDateTime(e.at, tz)}</td>
                  <td>
                    {e.kind === "uidvalidity-reset" ? (
                      <span className="badge">uidvalidity-reset</span>
                    ) : (
                      e.kind
                    )}
                  </td>
                  <td>{e.mailbox}</td>
                  <td className="small">{e.detail}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </TableScroll>
      )}
    </section>
  );
}

export function FailureReports({
  rows,
  tz,
}: {
  rows: FailureReport[];
  tz: string;
}) {
  return (
    <section className="card">
      <h2>Failure reports</h2>
      {rows.length === 0 ? (
        <Empty>No failure reports.</Empty>
      ) : (
        <TableScroll label="Failure reports">
          <table className="min-w-[42rem]" data-testid="failure-reports-table">
            <thead>
              <tr>
                <th>Arrival</th>
                <th>Domain</th>
                <th>Source IP</th>
                <th>Auth failure</th>
                <th>Headers (From / To / Subject)</th>
                <th>Message</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((f, i) => (
                <tr key={i}>
                  <td className="small">
                    {formatUnixDateTime(f.arrivalAt, tz)}
                  </td>
                  <td className="whitespace-nowrap">{f.domain}</td>
                  <td className="mono whitespace-nowrap">{f.sourceIp}</td>
                  <td>{f.authFailure}</td>
                  <td className="small">
                    {f.from} / {f.to} / {f.subject}
                  </td>
                  <td>
                    {f.messageId > 0 ? (
                      <a href={messageRawHref(f.messageId)} download>
                        Stored message
                      </a>
                    ) : (
                      "—"
                    )}
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
