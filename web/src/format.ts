// Pure formatting helpers (no DOM), unit-tested with node --test.
const numFmt = new Intl.NumberFormat("en-GB");
export function formatNumber(n: number): string {
  return numFmt.format(n);
}
export function formatPercent(
  rate: number | null | undefined,
  digits = 1,
): string {
  if (rate === null || rate === undefined || !Number.isFinite(rate)) return "—";
  return `${(rate * 100).toFixed(digits)}%`;
}
export function formatBytes(n: number): string {
  if (!Number.isFinite(n) || n < 0) return "—";
  if (n < 1024) return `${n} B`;
  const units = ["KiB", "MiB", "GiB"];
  let v = n / 1024;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(1)} ${units[i]}`;
}
// validDate turns unix seconds into a Date, or null when the value is
// missing or outside the range Date can represent.
function validDate(unixSeconds: number | null | undefined): Date | null {
  if (!unixSeconds || !Number.isFinite(unixSeconds)) return null;
  const date = new Date(unixSeconds * 1000);
  return Number.isNaN(date.getTime()) ? null : date;
}
export function formatUnixDate(
  unixSeconds: number | null | undefined,
  tz: string,
): string {
  const date = validDate(unixSeconds);
  if (!date) return "—";
  try {
    return new Intl.DateTimeFormat("en-GB", {
      timeZone: tz || "UTC",
      year: "numeric",
      month: "short",
      day: "numeric",
    }).format(date);
  } catch {
    return date.toISOString().slice(0, 10);
  }
}
export function formatUnixDateTime(
  unixSeconds: number | null | undefined,
  tz: string,
): string {
  const date = validDate(unixSeconds);
  if (!date) return "—";
  try {
    return new Intl.DateTimeFormat("en-GB", {
      timeZone: tz || "UTC",
      year: "numeric",
      month: "short",
      day: "numeric",
      hour: "2-digit",
      minute: "2-digit",
      hourCycle: "h23",
    }).format(date);
  } catch {
    return date.toISOString();
  }
}
export function formatUnixWindow(
  begin: number | null | undefined,
  end: number | null | undefined,
  tz: string,
): string {
  const start = validDate(begin);
  const finish = validDate(end);
  if (!start || !finish) return "—";
  try {
    const zone = tz || "UTC";
    const day = new Intl.DateTimeFormat("en-GB", {
      timeZone: zone,
      day: "numeric",
      month: "short",
    });
    const time = new Intl.DateTimeFormat("en-GB", {
      timeZone: zone,
      hour: "2-digit",
      minute: "2-digit",
      hourCycle: "h23",
    });
    const startDay = day.format(start);
    const endDay = day.format(finish);
    return startDay === endDay
      ? `${startDay} ${time.format(start)} – ${time.format(finish)}`
      : `${startDay} ${time.format(start)} – ${endDay} ${time.format(finish)}`;
  } catch {
    return `${start.toISOString()} – ${finish.toISOString()}`;
  }
}
export function formatDateLabel(date: string): string {
  // date is YYYY-MM-DD in tz; render without shifting the day.
  const [y, m, d] = date.split("-").map(Number);
  if (!y || !m || !d) return date;
  try {
    return new Intl.DateTimeFormat("en-GB", {
      timeZone: "UTC",
      year: "numeric",
      month: "short",
      day: "numeric",
    }).format(new Date(Date.UTC(y, m - 1, d)));
  } catch {
    return date;
  }
}
export function formatPending(n: number | null | undefined): string {
  return n === null || n === undefined ? "—" : formatNumber(n);
}

const sourceKindLabels: Record<string, string> = {
  rule: "Rule",
  as: "ASN",
  ptr: "PTR domain",
  ip: "IP",
};
export function sourceKindLabel(kind: string): string {
  return sourceKindLabels[kind] ?? kind;
}
export function plural(count: number, one: string, many: string): string {
  return count === 1 ? one : many;
}
