import type * as T from "./types.ts";

export class ApiError extends Error {
  code: string;
  status: number;
  constructor(status: number, code: string, message: string) {
    super(message);
    this.status = status;
    this.code = code;
  }
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  let res: Response;
  try {
    res = await fetch(path, init);
  } catch {
    throw new ApiError(
      0,
      "network",
      "Network error: could not reach the server.",
    );
  }
  if (!res.ok) {
    let code = "internal";
    let message = `Request failed (${res.status}).`;
    try {
      const body = (await res.json()) as T.ErrorBody;
      if (body.error) {
        code = body.error.code || code;
        message = body.error.message || message;
      }
    } catch {
      /* keep defaults */
    }
    throw new ApiError(res.status, code, message);
  }
  if (res.status === 204) return undefined as T;
  return (await res.json()) as T;
}

export interface RangeFilters {
  domain?: string;
  from?: string;
  to?: string;
  tz?: string;
}

function qs(params: Record<string, string | undefined | null>): string {
  const u = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) if (v) u.set(k, v);
  const s = u.toString();
  return s ? `?${s}` : "";
}
const rangeQs = (f: RangeFilters) =>
  qs({ domain: f.domain, from: f.from, to: f.to, tz: f.tz });

export const api = {
  domains: () => request<T.DomainsResponse>("/api/domains"),
  overview: (f: RangeFilters) =>
    request<T.Overview>(`/api/overview${rangeQs(f)}`),
  sources: (f: RangeFilters & { unknownOnly?: boolean }) =>
    request<T.SourcesResponse>(
      `/api/sources${qs({ domain: f.domain, from: f.from, to: f.to, tz: f.tz, unknown: f.unknownOnly ? "1" : undefined })}`,
    ),
  source: (key: string, f: RangeFilters) =>
    request<T.SourceDetail>(
      `/api/sources/${encodeURIComponent(key)}${rangeQs(f)}`,
    ),
  reports: (
    f: RangeFilters & {
      reporter?: string;
      kind?: string;
      verdict?: string;
      source?: string;
      cursor?: string;
      limit?: number;
    },
  ) =>
    request<T.ReportsResponse>(
      `/api/reports${qs({ domain: f.domain, from: f.from, to: f.to, tz: f.tz, reporter: f.reporter, kind: f.kind, verdict: f.verdict, source: f.source, cursor: f.cursor, limit: f.limit ? String(f.limit) : undefined })}`,
    ),
  report: (id: string | number, tz?: string) =>
    request<T.ReportDetail>(
      `/api/reports/${encodeURIComponent(String(id))}${qs({ tz })}`,
    ),
  reporters: (f: RangeFilters) =>
    request<T.ReportersResponse>(`/api/reporters${rangeQs(f)}`),
  tls: (f: RangeFilters) => request<T.TLSResponse>(`/api/tls${rangeQs(f)}`),
  tlsDomain: (domain: string, f: RangeFilters) =>
    request<T.TLSDomainDetail>(
      `/api/tls/${encodeURIComponent(domain)}${rangeQs(f)}`,
    ),
  status: () => request<T.Status>("/api/status"),
  fetchNow: () =>
    request<T.FetchResponse>("/api/fetch", {
      method: "POST",
      headers: { "X-Rua": "1" },
    }),
};
export const reportRawHref = (id: number | string) =>
  `/api/reports/${encodeURIComponent(String(id))}/raw`;
export const messageRawHref = (id: number | string) =>
  `/api/messages/${encodeURIComponent(String(id))}/raw`;
