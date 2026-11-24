import type { ReactNode } from "react";
import { ApiError } from "../api/client.ts";

export function Loading({ label = "Loading…" }: { label?: string }) { return <p role="status" data-testid="loading" className="muted" style={{ padding: 16 }}>{label}</p>; }
export function ErrorBox({ error }: { error: unknown }) {
  const msg = error instanceof ApiError ? `${error.message}${error.code ? ` (${error.code})` : ""}` : error instanceof Error ? error.message : "Something went wrong.";
  return <div role="alert" data-testid="error" className="card" style={{ borderColor: "var(--fail)" }}><strong>Error:</strong> {msg}</div>;
}
export function Empty({ children }: { children: ReactNode }) { return <p data-testid="empty" className="muted" style={{ padding: "12px 4px" }}>{children}</p>; }
export function VerdictBadge({ verdict }: { verdict: string }) {
  const cls = verdict === "pass" ? "badge badge-pass" : verdict === "forwarded" ? "badge badge-forwarded" : verdict === "fail" ? "badge badge-fail" : "badge";
  const label = verdict === "pass" ? "Pass" : verdict === "forwarded" ? "Forwarded" : verdict === "fail" ? "Fail" : verdict;
  return <span className={cls}>{label}</span>;
}
export function Legend() {
  return <div className="small muted" aria-label="Verdict legend" style={{ display: "flex", gap: 12, flexWrap: "wrap" }}><span><VerdictBadge verdict="pass" /></span><span><VerdictBadge verdict="forwarded" /></span><span><VerdictBadge verdict="fail" /></span><span>Forwarded is shown separately, not as failure.</span></div>;
}
