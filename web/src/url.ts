// Pure URL-state and date-range helpers. No DOM imports so node --test can load this file.

export type Page =
  | "overview"
  | "sources"
  | "source"
  | "reports"
  | "report"
  | "tls"
  | "tlsDomain"
  | "status"
  | "notFound";
export interface Filters {
  domain: string;
  from: string;
  to: string;
  tz: string;
  range: string;
  source: string;
  reporter: string;
  verdict: string;
  kind: string;
  unknownOnly: boolean;
  search: string;
}
export interface UrlState {
  page: Page;
  key: string;
  id: string;
  filters: Filters;
}

export const RANGE_PRESETS = ["7", "30", "90", "365", "custom"] as const;

export function emptyFilters(): Filters {
  return {
    domain: "",
    from: "",
    to: "",
    tz: "",
    range: "30",
    source: "",
    reporter: "",
    verdict: "",
    kind: "",
    unknownOnly: false,
    search: "",
  };
}

export function parseUrl(pathname: string, search: string): UrlState {
  const p = new URLSearchParams(
    search.startsWith("?") ? search.slice(1) : search,
  );
  const f = emptyFilters();
  f.domain = p.get("domain") ?? "";
  f.from = p.get("from") ?? "";
  f.to = p.get("to") ?? "";
  f.tz = p.get("tz") ?? "";
  f.range = p.get("range") ?? (f.from || f.to ? "custom" : "30");
  if (!RANGE_PRESETS.some((preset) => preset === f.range)) f.range = "30";
  f.source = p.get("source") ?? "";
  f.reporter = p.get("reporter") ?? "";
  f.verdict = p.get("verdict") ?? "";
  f.kind = p.get("kind") ?? "";
  f.search = p.get("q") ?? "";
  f.unknownOnly = p.get("unknown") === "1" || p.get("unknownOnly") === "1";
  const seg = pathname
    .split("/")
    .filter(Boolean)
    .map((s) => {
      try {
        return decodeURIComponent(s);
      } catch {
        return s;
      }
    });
  if (seg.length === 0)
    return { page: "overview", key: "", id: "", filters: f };
  if (seg[0] === "sources" && seg.length === 1)
    return { page: "sources", key: "", id: "", filters: f };
  if (seg[0] === "sources" && seg.length === 2)
    return { page: "source", key: seg[1], id: "", filters: f };
  if (seg[0] === "reports" && seg.length === 1)
    return { page: "reports", key: "", id: "", filters: f };
  if (seg[0] === "reports" && seg.length === 2)
    return { page: "report", key: "", id: seg[1], filters: f };
  if (seg[0] === "tls" && seg.length === 1)
    return { page: "tls", key: "", id: "", filters: f };
  if (seg[0] === "tls" && seg.length === 2)
    return { page: "tlsDomain", key: seg[1], id: "", filters: f };
  if (seg[0] === "status" && seg.length === 1)
    return { page: "status", key: "", id: "", filters: f };
  return { page: "notFound", key: "", id: "", filters: f };
}

export function pagePath(page: Page, key = "", id = ""): string {
  switch (page) {
    case "overview":
      return "/";
    case "sources":
      return "/sources";
    case "source":
      return `/sources/${encodeURIComponent(key)}`;
    case "reports":
      return "/reports";
    case "report":
      return `/reports/${encodeURIComponent(id)}`;
    case "tls":
      return "/tls";
    case "tlsDomain":
      return `/tls/${encodeURIComponent(key)}`;
    case "status":
      return "/status";
    default:
      return "/";
  }
}

export function serializeFilters(f: Filters): URLSearchParams {
  const p = new URLSearchParams();
  if (f.domain) p.set("domain", f.domain);
  if (f.from) p.set("from", f.from);
  if (f.to) p.set("to", f.to);
  if (f.tz) p.set("tz", f.tz);
  if (f.range && (f.range !== "30" || f.from || f.to)) p.set("range", f.range);
  if (f.source) p.set("source", f.source);
  if (f.reporter) p.set("reporter", f.reporter);
  if (f.verdict) p.set("verdict", f.verdict);
  if (f.kind) p.set("kind", f.kind);
  if (f.search) p.set("q", f.search);
  if (f.unknownOnly) p.set("unknown", "1");
  return p;
}

export function buildHref(state: UrlState): string {
  const q = serializeFilters(state.filters).toString();
  const path = pagePath(state.page, state.key, state.id);
  return q ? `${path}?${q}` : path;
}

/** Resolution order: URL tz, then stored, then browser, then UTC. */
export function resolveTimeZone(
  urlTz: string,
  stored: string | null,
  browser: string,
): string {
  for (const zone of [urlTz, stored, browser]) {
    if (!zone || !/^[A-Za-z][A-Za-z0-9_+/-]*$/.test(zone) || zone === "Local")
      continue;
    try {
      new Intl.DateTimeFormat("en", { timeZone: zone });
      return zone;
    } catch {
      // Skip invalid zones from URLs and older preferences.
    }
  }
  return "UTC";
}

function pad(n: number): string {
  return String(n).padStart(2, "0");
}

/** YYYY-MM-DD for an instant in tz, via Intl parts (DST-safe). */
export function dateInTimeZone(tz: string, instant: Date): string {
  try {
    const parts = new Intl.DateTimeFormat("en-US", {
      timeZone: tz,
      year: "numeric",
      month: "2-digit",
      day: "2-digit",
    }).formatToParts(instant);
    const get = (t: string) => parts.find((x) => x.type === t)?.value ?? "";
    return `${get("year")}-${get("month")}-${get("day")}`;
  } catch {
    return dateInTimeZoneUTC(instant);
  }
}
function dateInTimeZoneUTC(d: Date): string {
  return `${d.getUTCFullYear()}-${pad(d.getUTCMonth() + 1)}-${pad(d.getUTCDate())}`;
}

export function addDays(date: string, delta: number): string {
  const [y, m, d] = date.split("-").map(Number);
  const dt = new Date(Date.UTC(y, (m || 1) - 1, d || 1));
  dt.setUTCDate(dt.getUTCDate() + delta);
  return `${dt.getUTCFullYear()}-${pad(dt.getUTCMonth() + 1)}-${pad(dt.getUTCDate())}`;
}

/** Presets: today in tz inclusive, so 7 = [today-6, today]. Custom passes through. */
export function computeRange(
  range: string,
  tz: string,
  now: Date = new Date(),
  customFrom = "",
  customTo = "",
): { from: string; to: string } {
  if (range === "custom") {
    const today = dateInTimeZone(tz || "UTC", now);
    return { from: customFrom || today, to: customTo || today };
  }
  const n = RANGE_PRESETS.some((preset) => preset === range)
    ? Number(range)
    : 30;
  const to = dateInTimeZone(tz || "UTC", now);
  return { from: addDays(to, -(n - 1)), to };
}

/** Effective filters: fill tz and from/to for aggregating calls. */
export function effectiveFilters(
  f: Filters,
  tz: string,
  now: Date = new Date(),
): Filters {
  const r = computeRange(f.range, tz, now, f.from, f.to);
  return { ...f, tz, from: r.from, to: r.to };
}
export function supportedTimeZones(): string[] {
  try {
    const v = (
      Intl as unknown as { supportedValuesOf?: (k: string) => string[] }
    ).supportedValuesOf?.("timeZone");
    if (v?.length) return ["UTC", ...v.filter((zone) => zone !== "UTC")];
  } catch {
    /* fall through */
  }
  return ["UTC"];
}
