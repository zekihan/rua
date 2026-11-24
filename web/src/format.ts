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
export function formatUnixDate(
  unixSeconds: number | null | undefined,
  tz: string,
): string {
  if (!unixSeconds) return "—";
  try {
    return new Intl.DateTimeFormat("en-GB", {
      timeZone: tz || "UTC",
      year: "numeric",
      month: "short",
      day: "numeric",
    }).format(new Date(unixSeconds * 1000));
  } catch {
    return new Date(unixSeconds * 1000).toISOString().slice(0, 10);
  }
}
export function formatUnixDateTime(
  unixSeconds: number | null | undefined,
  tz: string,
): string {
  if (!unixSeconds) return "—";
  try {
    return new Intl.DateTimeFormat("en-GB", {
      timeZone: tz || "UTC",
      year: "numeric",
      month: "short",
      day: "numeric",
      hour: "2-digit",
      minute: "2-digit",
      hourCycle: "h23",
    }).format(new Date(unixSeconds * 1000));
  } catch {
    return new Date(unixSeconds * 1000).toISOString();
  }
}
export function formatUnixWindow(
  begin: number | null | undefined,
  end: number | null | undefined,
  tz: string,
): string {
  if (!begin || !end) return "—";
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
    const start = new Date(begin * 1000);
    const finish = new Date(end * 1000);
    const startDay = day.format(start);
    const endDay = day.format(finish);
    return startDay === endDay
      ? `${startDay} ${time.format(start)} – ${time.format(finish)}`
      : `${startDay} ${time.format(start)} – ${endDay} ${time.format(finish)}`;
  } catch {
    return `${new Date(begin * 1000).toISOString()} – ${new Date(end * 1000).toISOString()}`;
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
