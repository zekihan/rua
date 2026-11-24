import type { SourceRow } from "../../api/types.ts";
import type { PageProps } from "../../page-props.ts";
import { TableScroll } from "../../components/ui.tsx";
import { SplitBar } from "../../components/Verdict.tsx";
import { formatNumber, formatPercent } from "../../format.ts";

export type SortKey =
  "name" | "messages" | "spfAligned" | "dkimAligned" | "firstSeen" | "lastSeen";
export type Sort = { key: SortKey; direction: "ascending" | "descending" };

function SortHeader({
  field,
  sort,
  onSort,
  label,
  numeric,
}: {
  field: SortKey;
  sort: Sort;
  onSort: (key: SortKey) => void;
  label: string;
  numeric?: boolean;
}) {
  const direction = sort.key === field ? sort.direction : undefined;
  return (
    <th
      scope="col"
      aria-sort={direction}
      className={numeric ? "num" : undefined}
    >
      <button
        type="button"
        className="hover:text-accent"
        onClick={() => onSort(field)}
        aria-label={`Sort by ${label}`}
      >
        {label}{" "}
        <span
          aria-hidden="true"
          className={direction ? "text-accent" : "opacity-45"}
        >
          {direction === "ascending"
            ? "↑"
            : direction === "descending"
              ? "↓"
              : "↕"}
        </span>
      </button>
    </th>
  );
}

export function SourceTable({
  rows,
  sort,
  onSort,
  makeHref,
}: {
  rows: SourceRow[];
  sort: Sort;
  onSort: (key: SortKey) => void;
  makeHref: PageProps["makeHref"];
}) {
  const sortProps = { sort, onSort };
  return (
    <TableScroll label="Sources">
      <table className="min-w-[60rem] bg-surface" data-testid="sources-table">
        <thead>
          <tr>
            <SortHeader field="name" label="Source" {...sortProps} />
            <SortHeader
              field="messages"
              label="Messages"
              numeric
              {...sortProps}
            />
            <th scope="col">Pass / forwarded / fail</th>
            <SortHeader
              field="spfAligned"
              label="SPF aligned"
              numeric
              {...sortProps}
            />
            <SortHeader
              field="dkimAligned"
              label="DKIM aligned"
              numeric
              {...sortProps}
            />
            <th scope="col">DKIM domains</th>
            <SortHeader field="firstSeen" label="First seen" {...sortProps} />
            <SortHeader field="lastSeen" label="Last seen" {...sortProps} />
          </tr>
        </thead>
        <tbody>
          {rows.map((source) => (
            <tr
              key={source.key}
              className={source.expected ? undefined : "unknown-row"}
            >
              <td>
                <a href={makeHref("source", { key: source.key })}>
                  {source.name || source.key}
                </a>{" "}
                {!source.expected && (
                  <span
                    className="badge badge-unknown"
                    data-testid="unknown-source"
                  >
                    Unknown
                  </span>
                )}
                <p className="small muted">
                  {source.kind} · {formatNumber(source.ips)}{" "}
                  {source.ips === 1 ? "IP" : "IPs"}
                </p>
              </td>
              <td className="num">{formatNumber(source.messages)}</td>
              <td>
                <SplitBar {...source} />
                <p className="mt-1 text-xs tabular-nums text-ink-2">
                  {formatNumber(source.pass)} / {formatNumber(source.forwarded)}{" "}
                  / {formatNumber(source.fail)}
                </p>
              </td>
              <td className="num">{formatPercent(source.spfAligned)}</td>
              <td className="num">{formatPercent(source.dkimAligned)}</td>
              <td className="mono">{source.dkimDomains.join(", ") || "—"}</td>
              <td className="whitespace-nowrap tabular-nums">
                {source.firstSeen}
              </td>
              <td className="whitespace-nowrap tabular-nums">
                {source.lastSeen}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </TableScroll>
  );
}
