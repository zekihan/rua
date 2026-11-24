import { useEffect, useId, useRef, useState } from "react";
import type { DayVerdicts, PolicyChange, TLSDay } from "../api/types.ts";
import {
  barHeight,
  countScale,
  dayLabel,
  maxValue,
  tickIndices,
  visibleBarHeight,
} from "../chart-helpers.ts";
import { formatNumber } from "../format.ts";
import {
  VerdictPatterns,
  verdictFill,
  verdictSeries,
  type Verdict,
} from "./Verdict.tsx";
import { Empty, TableScroll } from "./ui.tsx";

type Series = ReadonlyArray<{ key: Verdict; label: string }>;
type ChartDay = { date: string; counts: Record<Verdict, number> };

function useChartWidth() {
  const ref = useRef<HTMLDivElement>(null);
  const [width, setWidth] = useState(640);
  useEffect(() => {
    if (!ref.current) return;
    const observer = new ResizeObserver(([entry]) => {
      setWidth(Math.max(180, Math.floor(entry.contentRect.width)));
    });
    observer.observe(ref.current);
    return () => observer.disconnect();
  }, []);
  return { ref, width };
}

export function policySummary(change: PolicyChange) {
  const { before, after, fields } = change;
  if (!before) return `First reported policy: ${after.p || "—"}`;
  return (
    fields
      .map((field) => {
        const key = field as keyof typeof after;
        return `${field}: ${before[key] === "" ? "—" : (before[key] ?? "—")} → ${after[key] === "" ? "—" : (after[key] ?? "—")}`;
      })
      .join(", ") || "Published policy changed"
  );
}

function DailyTable({
  days,
  series,
  label,
}: {
  days: ChartDay[];
  series: Series;
  label: string;
}) {
  return (
    <details data-testid="chart-table-toggle">
      <summary className="w-fit text-xs font-medium text-accent">
        Show as table
      </summary>
      <div className="mt-2">
        <TableScroll label={`${label} as a table`} className="max-h-72">
          <table data-testid="chart-table">
            <caption className="sr-only">{label}</caption>
            <thead className="sticky top-0">
              <tr>
                <th scope="col">Date</th>
                {series.map(({ key, label: name }) => (
                  <th key={key} scope="col" className="num">
                    {name}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {days.map(({ date, counts }) => (
                <tr key={date}>
                  <th scope="row" className="tabular-nums">
                    {date}
                  </th>
                  {series.map(({ key }) => (
                    <td key={key} className="num">
                      {formatNumber(counts[key])}
                    </td>
                  ))}
                </tr>
              ))}
            </tbody>
          </table>
        </TableScroll>
      </div>
    </details>
  );
}

function DailyChart({
  days,
  series,
  changes = [],
  label,
  compact = false,
  emptyText = "No activity in this range.",
}: {
  days: ChartDay[];
  series: Series;
  changes?: PolicyChange[];
  label: string;
  compact?: boolean;
  emptyText?: string;
}) {
  const { ref, width } = useChartWidth();
  const id = useId();
  const [activeDate, setActiveDate] = useState<string>();
  const [activeDay, setActiveDay] = useState<string>();
  const svgRef = useRef<SVGSVGElement>(null);
  const hintId = `${id}-hint`;
  const moveActive = (key: string) => {
    if (days.length === 0) return false;
    const current = days.findIndex((day) => day.date === activeDay);
    let next: number;
    if (key === "ArrowLeft") next = current < 0 ? days.length - 1 : current - 1;
    else if (key === "ArrowRight") next = current < 0 ? 0 : current + 1;
    else if (key === "Home") next = 0;
    else if (key === "End") next = days.length - 1;
    else return false;
    setActiveDate(undefined);
    setActiveDay(days[Math.max(0, Math.min(days.length - 1, next))].date);
    return true;
  };
  useEffect(() => {
    if (!activeDay) return;
    const close = (event: PointerEvent) => {
      if (
        !(event.target instanceof Element) ||
        !event.target.closest("[data-day]")
      )
        setActiveDay(undefined);
    };
    document.addEventListener("pointerdown", close);
    return () => document.removeEventListener("pointerdown", close);
  }, [activeDay]);
  const activeCounts = days.find((day) => day.date === activeDay);
  const changesByDate = new Map<string, PolicyChange[]>();
  for (const change of changes) {
    const existing = changesByDate.get(change.date);
    if (existing) existing.push(change);
    else changesByDate.set(change.date, [change]);
  }
  const policyDate = activeDate ?? activeDay;
  const active = policyDate ? (changesByDate.get(policyDate) ?? []) : [];
  const max = maxValue(
    days.map(({ counts }) =>
      series.reduce((sum, { key }) => sum + counts[key], 0),
    ),
  );
  const ticks = countScale(max);
  const limit = ticks.at(-1)!;
  const height = compact ? 140 : width < 500 ? 185 : 255;
  const top = 17;
  const bottom = height - 26;
  const left = Math.max(30, formatNumber(limit).length * 7 + 8);
  const right = width - 10;
  const slot = (right - left) / Math.max(1, days.length);
  const barWidth = Math.min(34, slot * 0.78);
  const midX = (i: number) => left + (i + 0.5) * slot;

  return (
    <div className="space-y-3" data-testid="daily-chart">
      <div className="flex flex-wrap items-center gap-x-4 gap-y-2 text-xs text-ink-2">
        {series.map(({ key, label: seriesLabel }) => (
          <span key={key} className="inline-flex items-center gap-1.5">
            <svg
              aria-hidden="true"
              width="12"
              height="12"
              className="rounded-xs"
            >
              <rect width="12" height="12" fill={verdictFill(id, key)} />
            </svg>
            {seriesLabel}
          </span>
        ))}
        {changes.length > 0 && (
          <span className="inline-flex items-center gap-1.5">
            <span aria-hidden="true" className="text-accent">
              ◆
            </span>{" "}
            Policy change
          </span>
        )}
      </div>
      <div ref={ref} className="relative min-w-0">
        {max === 0 && changes.length === 0 ? (
          <Empty>{emptyText}</Empty>
        ) : (
          <>
            <svg
              ref={svgRef}
              viewBox={`0 0 ${width} ${height}`}
              className="block w-full overflow-visible"
              role="group"
              tabIndex={0}
              aria-label={`${label}, ${days.length} ${days.length === 1 ? "day" : "days"}${days.length > 0 ? ` from ${days[0].date} to ${days[days.length - 1].date}` : ""}`}
              aria-describedby={hintId}
              data-testid="daily-chart-group"
              onFocus={(event) => {
                if (
                  event.target === event.currentTarget &&
                  event.currentTarget.matches(":focus-visible")
                )
                  setActiveDay((current) => current ?? days.at(-1)?.date);
              }}
              onBlur={(event) => {
                if (
                  !(event.relatedTarget instanceof Node) ||
                  !event.currentTarget.contains(event.relatedTarget)
                )
                  setActiveDay(undefined);
              }}
              onKeyDown={(event) => {
                if (event.key === "Escape") {
                  setActiveDay(undefined);
                  setActiveDate(undefined);
                } else if (moveActive(event.key)) event.preventDefault();
              }}
            >
              {ticks.map((tick) => {
                const y = bottom - barHeight(tick, limit, bottom - top);
                return (
                  <g key={tick} aria-hidden="true">
                    <line
                      x1={left}
                      x2={right}
                      y1={y}
                      y2={y}
                      stroke="var(--line)"
                    />
                    <text
                      x={left - 7}
                      y={y}
                      dominantBaseline="middle"
                      textAnchor="end"
                      className="fill-ink-2 text-[11px] tabular-nums"
                    >
                      {formatNumber(tick)}
                    </text>
                  </g>
                );
              })}
              {days.map(({ date, counts }, index) => {
                let y = bottom;
                const x = midX(index) - barWidth / 2;
                const selected = activeDay === date;
                return (
                  <g
                    key={date}
                    tabIndex={-1}
                    role="img"
                    aria-label={dayLabel(date, series, counts, formatNumber)}
                    data-testid="chart-day"
                    data-day={date}
                    className="outline-none"
                    onPointerEnter={(event) => {
                      if (event.pointerType !== "touch") setActiveDay(date);
                    }}
                    onPointerLeave={(event) => {
                      if (
                        event.pointerType !== "touch" &&
                        !svgRef.current?.contains(document.activeElement)
                      )
                        setActiveDay(undefined);
                    }}
                    onClick={() => setActiveDay(date)}
                  >
                    <rect
                      x={midX(index) - slot / 2}
                      y={top}
                      width={slot}
                      height={bottom - top}
                      fill={selected ? "var(--accent)" : "transparent"}
                      fillOpacity={selected ? 0.12 : 1}
                    />
                    {series.map(({ key }) => {
                      const size = visibleBarHeight(
                        counts[key],
                        limit,
                        bottom - top,
                      );
                      y -= size;
                      return (
                        size > 0 && (
                          <rect
                            key={key}
                            x={x}
                            y={y}
                            width={barWidth}
                            height={size}
                            fill={verdictFill(id, key)}
                          />
                        )
                      );
                    })}
                  </g>
                );
              })}
              {tickIndices(days.length, width < 440 ? 3 : 6).map((index) => {
                const x = Math.max(
                  left + 13,
                  Math.min(right - 13, midX(index)),
                );
                return (
                  <text
                    key={index}
                    x={x}
                    y={height - 7}
                    textAnchor="middle"
                    className="fill-ink-2 text-[11px] tabular-nums"
                  >
                    {days[index].date.slice(5)}
                  </text>
                );
              })}
              {Array.from(changesByDate, ([date, dated]) => {
                const index = days.findIndex((day) => day.date === date);
                if (index === -1) return null;
                const x = midX(index);
                return (
                  <g
                    key={date}
                    tabIndex={-1}
                    role="img"
                    aria-label={`Policy changes on ${date}: ${dated.map((change) => `${change.domain}, ${policySummary(change)}`).join("; ")}`}
                    data-testid="policy-marker"
                    className="cursor-help"
                    onFocus={() => setActiveDate(date)}
                    onBlur={() => setActiveDate(undefined)}
                    onPointerEnter={() => setActiveDate(date)}
                    onPointerLeave={(event) => {
                      if (document.activeElement !== event.currentTarget)
                        setActiveDate(undefined);
                    }}
                    onKeyDown={(event) => {
                      if (event.key === "Escape") setActiveDate(undefined);
                    }}
                  >
                    <line
                      x1={x}
                      y1={top}
                      x2={x}
                      y2={bottom}
                      stroke="var(--accent)"
                      strokeWidth="1.5"
                      strokeDasharray="4 4"
                      pointerEvents="none"
                    />
                    <rect
                      x={x - 9}
                      y="0"
                      width="18"
                      height="23"
                      fill="transparent"
                    />
                    <path
                      d={`M ${x} 4 l 5 5 -5 5 -5 -5 Z`}
                      fill="var(--accent)"
                      pointerEvents="none"
                    />
                  </g>
                );
              })}
            </svg>
            <p id={hintId} className="sr-only">
              Focus the chart, then use the left and right arrow keys to move
              between days, and Home or End to jump to the first or last day.
            </p>
            <p className="sr-only" aria-live="polite" data-testid="chart-live">
              {activeCounts &&
                `${dayLabel(activeCounts.date, series, activeCounts.counts, formatNumber)}${active.map((change) => `. Policy change for ${change.domain}: ${policySummary(change)}`).join("")}`}
            </p>
            {(active.length > 0 || activeCounts) && (
              <div
                role="tooltip"
                data-testid="chart-tooltip"
                className="pointer-events-none absolute top-6 right-2 z-10 max-w-[90%] space-y-1 rounded-lg border border-line bg-surface px-3 py-2 text-xs shadow-lg"
              >
                {activeCounts && (
                  <>
                    <p className="font-semibold">{activeCounts.date}</p>
                    <p className="tabular-nums">
                      {series
                        .map(
                          ({ key, label: name }) =>
                            `${name} ${formatNumber(activeCounts.counts[key])}`,
                        )
                        .join(" · ")}
                    </p>
                  </>
                )}
                {active.length > 0 && (
                  <>
                    {!activeCounts && (
                      <p className="font-semibold">{policyDate}</p>
                    )}
                    {active.map((change) => (
                      <p key={change.reportId}>
                        <strong>{change.domain}</strong> ·{" "}
                        {policySummary(change)}
                      </p>
                    ))}
                  </>
                )}
              </div>
            )}
          </>
        )}
      </div>
      {days.length > 0 && (
        <DailyTable days={days} series={series} label={label} />
      )}
      <svg className="absolute h-0 w-0" aria-hidden="true">
        <VerdictPatterns id={id} />
      </svg>
    </div>
  );
}

export function StackedBars({
  days,
  policyChanges,
}: {
  days: DayVerdicts[];
  policyChanges?: PolicyChange[];
}) {
  return (
    <DailyChart
      days={days.map((day) => ({ date: day.date, counts: day }))}
      series={verdictSeries}
      changes={policyChanges}
      label="Daily messages by DMARC verdict"
    />
  );
}

const tlsSeries = [
  { key: "pass", label: "Successful" },
  { key: "fail", label: "Failed" },
] as const;

const failedSeries = [{ key: "fail", label: "Failed" }] as const;

export function TLSBars({ days }: { days: TLSDay[] }) {
  return (
    <DailyChart
      days={days.map(({ date, success, failure }) => ({
        date,
        counts: { pass: success, forwarded: 0, fail: failure },
      }))}
      series={tlsSeries}
      label="Daily TLS sessions by outcome"
    />
  );
}

export function TLSFailureBars({ days }: { days: TLSDay[] }) {
  return (
    <DailyChart
      days={days.map(({ date, failure }) => ({
        date,
        counts: { pass: 0, forwarded: 0, fail: failure },
      }))}
      series={failedSeries}
      label="Daily failed TLS sessions"
      compact
      emptyText="No failed sessions in this range."
    />
  );
}
