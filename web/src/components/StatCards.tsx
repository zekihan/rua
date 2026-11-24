import type { ReactNode } from "react";

export type Stat = {
  label: string;
  value: ReactNode;
  id?: string;
  tone?: "pass" | "forwarded" | "fail";
};

const tones = {
  pass: "text-pass",
  forwarded: "text-forwarded",
  fail: "text-fail",
};

export function StatCards({
  stats,
  children,
}: {
  stats: Stat[];
  children?: ReactNode;
}) {
  return (
    <dl
      aria-label="Headline numbers"
      className="grid grid-cols-2 gap-2.5 sm:gap-3 lg:grid-cols-4"
    >
      {stats.map(({ label, value, id, tone }) => (
        <div
          key={label}
          className="card flex flex-col gap-1 py-2.5 sm:py-3.5"
          data-testid={id && `stat-${id}`}
        >
          <dt className="text-xs font-medium text-ink-2 sm:text-sm">{label}</dt>
          <dd
            className={`text-xl font-semibold tabular-nums tracking-tight sm:text-[1.65rem] ${tone ? tones[tone] : ""}`}
            data-testid={id && `stat-value-${id}`}
          >
            {value}
          </dd>
        </div>
      ))}
      {children}
    </dl>
  );
}
