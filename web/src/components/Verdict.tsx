import { useId } from "react";
import { stackedSegments } from "../chart-helpers.ts";
import { formatNumber } from "../format.ts";

export const verdictSeries = [
  { key: "pass", label: "Pass", symbol: "✓" },
  { key: "forwarded", label: "Forwarded", symbol: "↗" },
  { key: "fail", label: "Fail", symbol: "×" },
] as const;

export type Verdict = (typeof verdictSeries)[number]["key"];

export function VerdictBadge({ verdict }: { verdict: string }) {
  const entry = verdictSeries.find(({ key }) => key === verdict);
  return entry ? (
    <span className={`badge badge-${entry.key}`}>
      <span aria-hidden="true">{entry.symbol}</span>
      {entry.label}
    </span>
  ) : (
    <span className="badge">{verdict}</span>
  );
}

export function VerdictPatterns({ id }: { id: string }) {
  return (
    <defs>
      <pattern
        id={`${id}-forwarded`}
        width="5"
        height="5"
        patternUnits="userSpaceOnUse"
      >
        <rect width="5" height="5" fill="var(--forwarded)" opacity=".38" />
        <path
          d="M-1 1L1 -1M0 5L5 0M4 6L6 4"
          stroke="var(--forwarded)"
          strokeWidth="1.5"
        />
      </pattern>
      <pattern
        id={`${id}-fail`}
        width="5"
        height="5"
        patternUnits="userSpaceOnUse"
      >
        <rect width="5" height="5" fill="var(--fail)" opacity=".35" />
        <path d="M0 0L5 5M0 5L5 0" stroke="var(--fail)" strokeWidth="1.25" />
      </pattern>
    </defs>
  );
}

export function verdictFill(id: string, key: string) {
  return key === "pass" ? "var(--pass)" : `url(#${id}-${key})`;
}

type Counts = Record<Verdict, number>;

export function SplitBar(counts: Counts) {
  const id = useId();
  const { segments } = stackedSegments(
    verdictSeries.map(({ key }) => ({ key, value: counts[key] })),
  );
  const label = verdictSeries
    .map(({ key, label }) => `${label} ${formatNumber(counts[key])}`)
    .join(", ");
  return (
    <svg
      className="block h-2.5 w-full min-w-24 rounded-sm bg-surface-2"
      role="img"
      aria-label={label}
    >
      <VerdictPatterns id={id} />
      {segments
        .filter(({ value }) => value > 0)
        .map(({ key, offset, fraction }) => (
          <rect
            key={key}
            x={`${offset * 100}%`}
            width={`${fraction * 100}%`}
            height="100%"
            fill={verdictFill(id, key)}
          />
        ))}
    </svg>
  );
}
