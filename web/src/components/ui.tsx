import type { ReactNode } from "react";
import { ApiError } from "../api/client.ts";

export function PageHeading({
  children,
  testid,
}: {
  children: ReactNode;
  testid?: string;
}) {
  return (
    <h1
      className="text-2xl font-bold tracking-tight [overflow-wrap:anywhere] sm:text-[1.75rem]"
      data-testid={testid}
    >
      {children}
    </h1>
  );
}

export function Field({
  label,
  children,
}: {
  label: string;
  children: ReactNode;
}) {
  return (
    <label className="grid min-w-0 gap-1 text-xs font-semibold text-ink-2">
      {label}
      {children}
    </label>
  );
}

export function Loading({ label = "Loading…" }: { label?: string }) {
  return (
    <p role="status" data-testid="loading" className="card text-ink-2">
      {label}
    </p>
  );
}

export function ErrorBox({ error }: { error: unknown }) {
  const message =
    error instanceof ApiError || error instanceof Error
      ? error.message
      : "Something went wrong.";
  return (
    <p
      role="alert"
      data-testid="error"
      className="card border-fail/40 text-fail"
    >
      <strong>Could not load data.</strong> {message}
    </p>
  );
}

export function Empty({ children }: { children: ReactNode }) {
  return (
    <p
      data-testid="empty"
      className="rounded-md border border-dashed border-line px-3 py-3 text-sm text-ink-2"
    >
      {children}
    </p>
  );
}

export function TableScroll({
  children,
  label,
}: {
  children: ReactNode;
  label: string;
}) {
  return (
    <div className="table-wrap" role="region" tabIndex={0} aria-label={label}>
      {children}
    </div>
  );
}
