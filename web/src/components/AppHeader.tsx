import { useQuery } from "@tanstack/react-query";
import { api } from "../api/client.ts";
import { storeTimeZone } from "../hooks/useNavigation.ts";
import type { PageProps } from "../page-props.ts";
import {
  RANGE_PRESETS,
  supportedTimeZones,
  type Filters,
  type Page,
} from "../url.ts";
import { Field } from "./ui.tsx";

const nav = [
  { page: "overview", label: "Overview", pages: ["overview"] },
  { page: "sources", label: "Sources", pages: ["sources", "source"] },
  { page: "reports", label: "Reports", pages: ["reports", "report"] },
  { page: "tls", label: "TLS", pages: ["tls", "tlsDomain"] },
  { page: "status", label: "Status", pages: ["status"] },
] satisfies { page: Page; label: string; pages: string[] }[];

const zones = supportedTimeZones();

export function AppHeader({ state, eff, navigate, makeHref, tz }: PageProps) {
  const { page, key, id, filters } = state;
  const showRange = [
    "overview",
    "sources",
    "source",
    "reports",
    "tls",
    "tlsDomain",
  ].includes(page);
  const showDomain = [
    "overview",
    "sources",
    "source",
    "reports",
    "tls",
  ].includes(page);
  const domains = useQuery({
    queryKey: ["domains"],
    queryFn: api.domains,
    enabled: showDomain,
  });
  const domainOptions = domains.data?.domains.map(({ domain }) => domain) ?? [];
  if (filters.domain && !domainOptions.includes(filters.domain))
    domainOptions.unshift(filters.domain);
  const update = (patch: Partial<Filters>) =>
    navigate(makeHref(page, { key, id, patch }));

  const zonePicker = (
    <Field label="Time zone">
      <select
        className={showRange ? "input md:w-52" : "input w-40 md:w-52"}
        data-testid="tz-picker"
        value={tz}
        onChange={(e) => {
          storeTimeZone(e.target.value);
          update({ tz: e.target.value });
        }}
      >
        {(zones.includes(tz) ? zones : [tz, ...zones]).map((zone) => (
          <option key={zone}>{zone}</option>
        ))}
      </select>
    </Field>
  );

  return (
    <header className="border-b border-line bg-surface">
      <div className="shell flex flex-wrap items-center gap-x-7 md:flex-nowrap">
        <a
          href={makeHref("overview")}
          className="flex min-h-14 items-center gap-2.5 font-semibold text-ink"
        >
          <img src="/icon.svg" width="28" height="28" alt="" />
          <span className="text-xl tracking-tight">rua</span>
        </a>
        <nav
          aria-label="Primary"
          className="order-last flex w-full items-stretch justify-between gap-1 md:order-none md:w-auto md:justify-start"
        >
          {nav.map(({ page: target, label, pages }) => (
            <a
              key={target}
              href={makeHref(target)}
              data-testid={`nav-${target}`}
              aria-current={pages.includes(page) ? "page" : undefined}
              className="border-b-2 border-transparent px-2 py-3 text-sm font-medium text-ink-2 hover:text-accent aria-[current=page]:border-accent aria-[current=page]:text-accent md:px-3 md:py-[17px]"
            >
              {label}
            </a>
          ))}
        </nav>
        {!showRange && (
          <div className="ml-auto min-w-0 py-1.5 md:order-last">
            {zonePicker}
          </div>
        )}
      </div>
      {showRange && (
        <div className="border-t border-line bg-bg">
          <div className="shell grid grid-cols-2 items-end gap-2.5 py-3 md:flex md:flex-wrap md:gap-3">
            {showDomain && (
              <Field label="Domain">
                <select
                  className="input md:w-48"
                  data-testid="domain-picker"
                  value={filters.domain}
                  onChange={(e) => update({ domain: e.target.value })}
                >
                  <option value="">All domains</option>
                  {domainOptions.map((domain) => (
                    <option key={domain}>{domain}</option>
                  ))}
                </select>
                {domains.isError && (
                  <span className="text-xs text-fail">
                    Could not load domains.
                  </span>
                )}
              </Field>
            )}
            <Field label="Range">
              <select
                className="input md:w-36"
                data-testid="range-picker"
                value={filters.range}
                onChange={(e) => {
                  const range = e.target.value;
                  update(
                    range === "custom"
                      ? {
                          range,
                          from: filters.from || eff.from,
                          to: filters.to || eff.to,
                        }
                      : { range },
                  );
                }}
              >
                {RANGE_PRESETS.map((range) => (
                  <option key={range} value={range}>
                    {range === "custom" ? "Custom" : `${range} days`}
                  </option>
                ))}
              </select>
            </Field>
            {filters.range === "custom" && (
              <>
                <Field label="From date">
                  <input
                    type="date"
                    className="input md:w-40"
                    data-testid="range-from"
                    value={eff.from}
                    max={eff.to}
                    onChange={(e) => {
                      if (e.target.value) update({ from: e.target.value });
                    }}
                  />
                </Field>
                <Field label="To date">
                  <input
                    type="date"
                    className="input md:w-40"
                    data-testid="range-to"
                    value={eff.to}
                    min={eff.from}
                    onChange={(e) => {
                      if (e.target.value) update({ to: e.target.value });
                    }}
                  />
                </Field>
              </>
            )}
            {zonePicker}
            <span
              className={`${showDomain ? "" : "col-span-2"} self-end pb-1.5 text-right text-xs tabular-nums text-ink-2 md:ml-auto`}
              data-testid="range-label"
            >
              {eff.from} to {eff.to} <span className="sr-only">({tz})</span>
            </span>
          </div>
        </div>
      )}
    </header>
  );
}
