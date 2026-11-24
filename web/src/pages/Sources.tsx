import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { api } from "../api/client.ts";
import type { SourceRow } from "../api/types.ts";
import type { PageProps } from "../page-props.ts";
import {
  Empty,
  ErrorBox,
  Field,
  Loading,
  PageHeading,
} from "../components/ui.tsx";
import {
  SourceTable,
  type Sort,
  type SortKey,
} from "./sources/SourceTable.tsx";

function compare(a: SourceRow, b: SourceRow, sort: Sort) {
  const first = a[sort.key];
  const second = b[sort.key];
  if (first === second) return a.key.localeCompare(b.key);
  if (first === null) return 1;
  if (second === null) return -1;
  const order =
    typeof first === "number" && typeof second === "number"
      ? first - second
      : String(first).localeCompare(String(second));
  return sort.direction === "ascending" ? order : -order;
}

export default function Sources({ state, eff, makeHref, navigate }: PageProps) {
  const { search, unknownOnly } = state.filters;
  const filters = {
    from: eff.from,
    to: eff.to,
    tz: eff.tz,
    domain: eff.domain,
    unknownOnly,
  };
  const query = useQuery({
    queryKey: ["sources", filters],
    queryFn: () => api.sources(filters),
  });
  const [sort, setSort] = useState<Sort>({
    key: "messages",
    direction: "descending",
  });
  const needle = search.trim().toLowerCase();
  const rows = query.data?.sources
    .filter((source) =>
      [source.name, source.key, ...source.dkimDomains]
        .join(" ")
        .toLowerCase()
        .includes(needle),
    )
    .sort((a, b) => compare(a, b, sort));

  function onSort(key: SortKey) {
    setSort((current) => ({
      key,
      direction:
        key === current.key && current.direction === "descending"
          ? "ascending"
          : "descending",
    }));
  }

  return (
    <div className="page">
      <PageHeading testid="sources-heading">Sources</PageHeading>
      <div className="grid grid-cols-[minmax(0,1fr)_auto] items-end gap-2.5 sm:flex sm:gap-3">
        <Field label="Filter sources">
          <input
            className="input sm:w-64"
            data-testid="sources-filter"
            type="search"
            value={search}
            onChange={(e) =>
              navigate(
                makeHref("sources", { patch: { search: e.target.value } }),
                true,
              )
            }
            placeholder="Name or DKIM domain"
          />
        </Field>
        <label className="flex min-h-10 items-center gap-2 rounded-md border border-line bg-surface px-2.5 text-xs font-medium sm:px-3 sm:text-sm">
          <input
            type="checkbox"
            className="size-4 accent-accent"
            data-testid="unknown-only-toggle"
            checked={unknownOnly}
            onChange={(e) =>
              navigate(
                makeHref("sources", {
                  patch: { unknownOnly: e.target.checked },
                }),
              )
            }
          />
          Unknown only
        </label>
      </div>
      {!rows ? (
        query.isError ? (
          <ErrorBox error={query.error} />
        ) : (
          <Loading />
        )
      ) : rows.length === 0 ? (
        <Empty>
          {search
            ? "No sources match this filter."
            : "No sources in this range."}
        </Empty>
      ) : (
        <SourceTable
          rows={rows}
          sort={sort}
          onSort={onSort}
          makeHref={makeHref}
        />
      )}
    </div>
  );
}
