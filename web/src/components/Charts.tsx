import type { DayVerdicts, TLSDay, PolicyChange } from "../api/types.ts";
import { stackedSegments, maxValue, barHeight, donutArcs, sparklinePoints, polar } from "../chart-helpers.ts";
import { formatNumber } from "../format.ts";

export const VERDICT_LABEL: Record<string, string> = { pass: "Pass", forwarded: "Forwarded", fail: "Fail" };

export function SplitBar({ pass, forwarded, fail }: { pass: number; forwarded: number; fail: number }) {
  const { segments } = stackedSegments([{ key: "pass", value: pass }, { key: "forwarded", value: forwarded }, { key: "fail", value: fail }]);
  const color: Record<string, string> = { pass: "var(--pass)", forwarded: "var(--forwarded)", fail: "var(--fail)" };
  return (
    <div className="splitbar" role="img" aria-label={`Pass ${formatNumber(pass)}, forwarded ${formatNumber(forwarded)}, fail ${formatNumber(fail)}`}>
      {segments.map((s) => <span key={s.key} style={{ width: `${s.fraction * 100}%`, background: color[s.key] }} />)}
    </div>
  );
}

export function Donut({ pass, forwarded, fail, size = 96 }: { pass: number; forwarded: number; fail: number; size?: number }) {
  const arcs = donutArcs([{ key: "pass", value: pass }, { key: "forwarded", value: forwarded }, { key: "fail", value: fail }]);
  const color: Record<string, string> = { pass: "var(--pass)", forwarded: "var(--forwarded)", fail: "var(--fail)" };
  const r = 38; const cx = 50; const cy = 50;
  const total = pass + forwarded + fail;
  return (
    <svg width={size} height={size} viewBox="0 0 100 100" role="img" aria-label={`Pass/forwarded/fail donut: pass ${formatNumber(pass)}, forwarded ${formatNumber(forwarded)}, fail ${formatNumber(fail)}`}>
      <title>{`Pass ${formatNumber(pass)}, forwarded ${formatNumber(forwarded)}, fail ${formatNumber(fail)}`}</title>
      <defs><pattern id="donut-fwd" width="6" height="6" patternUnits="userSpaceOnUse" patternTransform="rotate(45)"><rect width="6" height="6" fill="var(--forwarded)" opacity="0.35" /><line x1="0" y1="0" x2="0" y2="6" stroke="var(--forwarded)" strokeWidth="3" /></pattern></defs>
      {total === 0 ? <circle cx={cx} cy={cy} r={r} fill="none" stroke="var(--line)" strokeWidth="12" /> : arcs.filter((a) => a.fraction > 0).map((a) => {
        if (a.fraction >= 0.999) return <circle key={a.key} cx={cx} cy={cy} r={r} fill="none" stroke={a.key === "forwarded" ? "url(#donut-fwd)" : color[a.key]} strokeWidth="12" />;
        const [x1, y1] = polar(cx, cy, r, a.startAngle); const [x2, y2] = polar(cx, cy, r, a.endAngle);
        const large = a.fraction > 0.5 ? 1 : 0;
        return <path key={a.key} d={`M ${x1} ${y1} A ${r} ${r} 0 ${large} 1 ${x2} ${y2}`} fill="none" stroke={a.key === "forwarded" ? "url(#donut-fwd)" : color[a.key]} strokeWidth="12" />;
      })}
    </svg>
  );
}

export function Sparkline({ values, label }: { values: number[]; label: string }) {
  const pts = sparklinePoints(values, 120, 32);
  return <svg width={120} height={32} viewBox="0 0 120 32" role="img" aria-label={label}><title>{label}</title>{pts && <polyline points={pts} fill="none" stroke="var(--accent)" strokeWidth="2" />}</svg>;
}

export function StackedBars({ days, policyChanges = [] }: { days: DayVerdicts[]; policyChanges?: PolicyChange[] }) {
  const W = 720; const H = 180; const padB = 18; const chartH = H - padB;
  const max = maxValue(days.map((d) => d.messages)) || 1;
  const bw = days.length ? W / days.length : W;
  const changeDates = new Set(policyChanges.map((c) => c.date));
  const summary = `Daily messages by verdict over ${days.length} days, total ${formatNumber(days.reduce((a, d) => a + d.messages, 0))}`;
  return (
    <svg viewBox={`0 0 ${W} ${H}`} style={{ width: "100%", height: "auto", minWidth: 320 }} role="img" aria-label={summary}>
      <title>{summary}</title>
      <defs>
        <pattern id="fwd-hatch" width="6" height="6" patternUnits="userSpaceOnUse" patternTransform="rotate(45)"><rect width="6" height="6" fill="var(--forwarded)" opacity="0.55" /><line x1="0" y1="0" x2="0" y2="6" stroke="var(--forwarded)" strokeWidth="3" /></pattern>
        <pattern id="fail-hatch" width="7" height="7" patternUnits="userSpaceOnUse"><rect width="7" height="7" fill="var(--fail)" opacity="0.5" /><path d="M0 0L7 7M7 0L0 7" stroke="var(--fail)" strokeWidth="1.2" /></pattern>
      </defs>
      {days.map((d, i) => {
        const x = i * bw; let y = chartH;
        const parts = [
          { key: "pass", v: d.pass, fill: "var(--pass)" }, { key: "forwarded", v: d.forwarded, fill: "url(#fwd-hatch)" }, { key: "fail", v: d.fail, fill: "url(#fail-hatch)" },
        ];
        return (
          <g key={d.date}>
            <title>{`${d.date}: pass ${formatNumber(d.pass)}, forwarded ${formatNumber(d.forwarded)}, fail ${formatNumber(d.fail)}`}</title>
            {parts.map((p) => { const h = barHeight(p.v, max, chartH); y -= h; return p.v ? <rect key={p.key} x={x + 1} y={y} width={Math.max(1, bw - 2)} height={h} fill={p.fill} /> : null; })}
            {changeDates.has(d.date) && <g><line x1={x + bw / 2} y1={0} x2={x + bw / 2} y2={chartH} stroke="var(--accent)" strokeWidth="1.5" strokeDasharray="3 2" /><path d={`M ${x + bw / 2 - 4} 0 L ${x + bw / 2 + 4} 0 L ${x + bw / 2} 7 Z`} fill="var(--accent)" /></g>}
            {(i === 0 || i === days.length - 1 || (days.length > 14 && i % Math.ceil(days.length / 6) === 0)) && <text x={x} y={H - 4} fontSize="9" fill="var(--ink-2)">{d.date.slice(5)}</text>}
          </g>
        );
      })}
    </svg>
  );
}

export function TLSBars({ days }: { days: TLSDay[] }) {
  const W = 720; const H = 160; const chartH = H - 18; const max = maxValue(days.map((d) => d.success + d.failure)) || 1; const bw = days.length ? W / days.length : W;
  const summary = `Daily TLS sessions over ${days.length} days: success ${formatNumber(days.reduce((a, d) => a + d.success, 0))}, failure ${formatNumber(days.reduce((a, d) => a + d.failure, 0))}`;
  return (
    <svg viewBox={`0 0 ${W} ${H}`} style={{ width: "100%", height: "auto", minWidth: 320 }} role="img" aria-label={summary}>
      <title>{summary}</title>
      <defs><pattern id="tls-fail" width="7" height="7" patternUnits="userSpaceOnUse"><rect width="7" height="7" fill="var(--fail)" opacity="0.5" /><path d="M0 0L7 7M7 0L0 7" stroke="var(--fail)" strokeWidth="1.2" /></pattern></defs>
      {days.map((d, i) => { const x = i * bw; const sh = barHeight(d.success, max, chartH); const fh = barHeight(d.failure, max, chartH); return (
        <g key={d.date}><title>{`${d.date}: success ${formatNumber(d.success)}, failure ${formatNumber(d.failure)}`}</title>
          {d.success > 0 && <rect x={x + 1} y={chartH - sh} width={Math.max(1, bw - 2)} height={sh} fill="var(--success)" />}
          {d.failure > 0 && <rect x={x + 1} y={chartH - sh - fh} width={Math.max(1, bw - 2)} height={fh} fill="url(#tls-fail)" />}
          {(i === 0 || i === days.length - 1) && <text x={x} y={H - 4} fontSize="9" fill="var(--ink-2)">{d.date.slice(5)}</text>}</g>); })}
    </svg>
  );
}
