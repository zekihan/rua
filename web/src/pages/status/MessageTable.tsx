import { messageRawHref } from "../../api/client.ts";
import type { MessageRow } from "../../api/types.ts";
import { Empty, TableScroll } from "../../components/ui.tsx";
import { formatBytes, formatUnixDateTime } from "../../format.ts";

export function MessageTable({
  rows,
  testid,
  title,
  tz,
}: {
  rows: MessageRow[];
  testid: string;
  title: string;
  tz: string;
}) {
  return (
    <section className="card">
      <h2>{title}</h2>
      {rows.length === 0 ? (
        <Empty>No messages.</Empty>
      ) : (
        <>
          <ul className="grid gap-2 sm:hidden" data-testid={`${testid}-cards`}>
            {rows.map((message) => (
              <li
                key={message.id}
                data-testid="message-card"
                className="space-y-1 rounded-lg border border-line bg-surface p-3"
              >
                <p className="font-semibold [overflow-wrap:anywhere]">
                  {message.from || "—"}
                </p>
                <p className="text-xs text-ink-2 [overflow-wrap:anywhere]">
                  {message.subject || "—"}
                </p>
                <p className="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs">
                  <span
                    className={
                      message.status === "ok" ? "badge" : "badge badge-fail"
                    }
                  >
                    {message.status}
                  </span>
                  <span>{message.class}</span>
                </p>
                {message.error && (
                  <p
                    data-testid="message-error"
                    className="text-xs text-fail [overflow-wrap:anywhere]"
                  >
                    {message.error}
                  </p>
                )}
                <p className="text-xs text-ink-2 tabular-nums">
                  {formatUnixDateTime(message.internalDateAt, tz)} ·{" "}
                  {formatBytes(message.size)} · UID {message.uid}
                  {message.mailbox ? ` · ${message.mailbox}` : ""}
                </p>
                {message.hasRaw && (
                  <a
                    className="text-xs"
                    href={messageRawHref(message.id)}
                    download
                  >
                    Raw message
                  </a>
                )}
              </li>
            ))}
          </ul>
          <TableScroll label={title} className="max-sm:hidden">
            <table className="min-w-[46rem]" data-testid={testid}>
              <thead>
                <tr>
                  <th>Date</th>
                  <th>From / subject</th>
                  <th>Mailbox</th>
                  <th>Class / status</th>
                  <th>Error</th>
                  <th>Download</th>
                </tr>
              </thead>
              <tbody>
                {rows.map((message) => (
                  <tr key={message.id}>
                    <td className="text-xs whitespace-nowrap tabular-nums">
                      {formatUnixDateTime(message.internalDateAt, tz)}
                      <p className="text-ink-2">
                        {formatBytes(message.size)} · UID {message.uid}
                      </p>
                    </td>
                    <td>
                      {message.from}
                      <p className="text-xs text-ink-2">{message.subject}</p>
                    </td>
                    <td>{message.mailbox || "—"}</td>
                    <td>
                      {message.class}{" "}
                      <span
                        className={
                          message.status === "ok" ? "badge" : "badge badge-fail"
                        }
                      >
                        {message.status}
                      </span>
                    </td>
                    <td className="text-xs">{message.error || "—"}</td>
                    <td>
                      {message.hasRaw ? (
                        <a
                          className="whitespace-nowrap"
                          href={messageRawHref(message.id)}
                          download
                        >
                          Raw message
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
        </>
      )}
    </section>
  );
}
