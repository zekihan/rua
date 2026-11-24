// Pure chart scale helpers (no DOM), unit-tested.
export interface Segment {
  key: string;
  value: number;
  offset: number;
  fraction: number;
}
export function stackedSegments(values: { key: string; value: number }[]): {
  total: number;
  segments: Segment[];
} {
  const total = values.reduce((a, v) => a + Math.max(0, v.value), 0);
  let off = 0;
  const segments = values.map((v) => {
    const value = Math.max(0, v.value);
    const fraction = total ? value / total : 0;
    const s = { key: v.key, value, offset: off, fraction };
    off += fraction;
    return s;
  });
  return { total, segments };
}
export function maxValue(values: number[]): number {
  return values.reduce((a, v) => Math.max(a, v), 0);
}
export function barHeight(value: number, max: number, height: number): number {
  if (!max || !value) return 0;
  return (value / max) * height;
}
export function donutArcs(values: { key: string; value: number }[]): {
  key: string;
  value: number;
  startAngle: number;
  endAngle: number;
  fraction: number;
}[] {
  const { total, segments } = stackedSegments(values);
  return segments.map((s) => ({
    key: s.key,
    value: s.value,
    fraction: s.fraction,
    startAngle: total ? s.offset * Math.PI * 2 : 0,
    endAngle: total ? (s.offset + s.fraction) * Math.PI * 2 : 0,
  }));
}
export function sparklinePoints(
  values: number[],
  width: number,
  height: number,
  pad = 2,
): string {
  if (!values.length) return "";
  const max = maxValue(values);
  const min = Math.min(...values);
  const span = max - min || 1;
  const step = values.length > 1 ? (width - pad * 2) / (values.length - 1) : 0;
  return values
    .map(
      (v, i) =>
        `${(pad + i * step).toFixed(2)},${(height - pad - ((v - min) / span) * (height - pad * 2)).toFixed(2)}`,
    )
    .join(" ");
}
export function polar(
  cx: number,
  cy: number,
  r: number,
  angle: number,
): [number, number] {
  return [cx + r * Math.sin(angle), cy - r * Math.cos(angle)];
}

export function countScale(max: number, intervals = 4): number[] {
  const target = Math.max(1, max / intervals);
  const power = 10 ** Math.floor(Math.log10(target));
  const step =
    [1, 2, 2.5, 5, 10]
      .map((n) => n * power)
      .find((n) => Number.isInteger(n) && n >= target) ?? 10 * power;
  return Array.from(
    { length: Math.ceil(Math.max(max, 1) / step) + 1 },
    (_, i) => i * step,
  );
}

export function tickIndices(length: number, limit: number): number[] {
  const count = Math.min(length, limit);
  if (count < 2) return count === 1 ? [0] : [];
  return Array.from({ length: count }, (_, i) =>
    Math.round((i * (length - 1)) / (count - 1)),
  );
}
