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
        <TableScroll label={title}>
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
      )}
    </section>
  );
}
