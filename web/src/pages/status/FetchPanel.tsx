import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api, ApiError } from "../../api/client.ts";
import type { Status } from "../../api/types.ts";
import { formatNumber, formatUnixDateTime } from "../../format.ts";

function fetchMessage(error: Error, cooldownUntil: string) {
  if (error instanceof ApiError) {
    switch (error.code) {
      case "fetch_running":
        return "A fetch is already running.";
      case "fetch_cooldown":
        return cooldownUntil === "—"
          ? "Fetch is in cooldown."
          : `Fetch is in cooldown until ${cooldownUntil}.`;
      case "fetch_disabled":
        return "Fetching is disabled (IMAP not configured).";
    }
  }
  return error.message;
}

export function FetchPanel({ status, tz }: { status: Status; tz: string }) {
  const queryClient = useQueryClient();
  const fetchNow = useMutation({
    mutationFn: api.fetchNow,
    onSettled: () =>
      void queryClient.invalidateQueries({ queryKey: ["status"] }),
  });
  const {
    fetch,
    lastSuccessAt,
    parserVersion,
    reparsing,
    enrichQueue,
    duplicates,
  } = status;
  const cooldownUntil = formatUnixDateTime(fetch.cooldownUntilAt, tz);
  const state = fetch.running
    ? "Running"
    : !fetch.enabled
      ? "Disabled"
      : "Ready";
  const message = fetchNow.error
    ? fetchMessage(fetchNow.error, cooldownUntil)
    : fetchNow.data?.status === "accepted"
      ? "Fetch started."
      : "";
  const fields = [
    ["Last successful fetch", formatUnixDateTime(lastSuccessAt, tz)],
    ["Next scheduled fetch", formatUnixDateTime(fetch.nextFetchAt, tz)],
    ["Cooldown until", cooldownUntil],
    ["Enrichment queue", formatNumber(enrichQueue)],
    [
      "Parser version",
      `${parserVersion} · ${reparsing ? "Reparsing" : "Idle"}`,
    ],
    ["Duplicate reports", formatNumber(duplicates)],
  ];

  return (
    <section className="card">
      <div className="mb-4 flex flex-wrap items-center justify-between gap-2">
        <div className="flex items-center gap-2">
          <h2 className="text-base font-semibold tracking-tight">
            Mailbox fetch
          </h2>
          <span className={fetch.enabled ? "badge badge-pass" : "badge"}>
            {state}
          </span>
        </div>
        <button
          type="button"
          className="btn"
          data-testid="fetch-now"
          disabled={!fetch.enabled || fetch.running || fetchNow.isPending}
          aria-describedby={fetch.enabled ? undefined : "fetch-disabled-note"}
          onClick={() => fetchNow.mutate()}
        >
          {fetchNow.isPending ? "Requesting…" : "Fetch now"}
        </button>
      </div>
      <dl className="grid grid-cols-2 gap-px overflow-hidden rounded-lg border border-line bg-line lg:grid-cols-3">
        {fields.map(([label, value]) => (
          <div key={label} className="min-w-0 bg-surface p-2.5 sm:p-3">
            <dt className="text-xs font-medium text-ink-2">{label}</dt>
            <dd className="mt-0.5 font-semibold tabular-nums [overflow-wrap:anywhere]">
              {value}
            </dd>
          </div>
        ))}
      </dl>
      {message ? (
        <p
          role="status"
          data-testid="fetch-message"
          className="mt-3 rounded-md bg-surface-2 px-3 py-2 text-sm"
        >
          {message}
        </p>
      ) : (
        !fetch.enabled && (
          <p
            id="fetch-disabled-note"
            data-testid="fetch-disabled-note"
            className="mt-3 text-xs text-ink-2"
          >
            Fetch now is unavailable: IMAP is not configured, so there is no
            mailbox to read. Stored reports remain available.
          </p>
        )
      )}
    </section>
  );
}
