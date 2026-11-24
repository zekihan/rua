import { useCallback, useEffect, useMemo, useState } from "react";
import type { PageProps } from "../page-props.ts";
import {
  buildHref,
  effectiveFilters,
  parseUrl,
  resolveTimeZone,
} from "../url.ts";

const TZ_KEY = "rua.tz";

function readLocation() {
  return parseUrl(window.location.pathname, window.location.search);
}

function readStoredZone() {
  try {
    return window.localStorage.getItem(TZ_KEY);
  } catch {
    return null;
  }
}

export function storeTimeZone(tz: string) {
  try {
    window.localStorage.setItem(TZ_KEY, tz);
  } catch {
    // The URL still carries the preference when storage is unavailable.
  }
}

export function useNavigation(): PageProps {
  const [state, setState] = useState(readLocation);
  const navigate = useCallback((href: string, replace = false) => {
    if (href === window.location.pathname + window.location.search) return;
    if (replace) window.history.replaceState(null, "", href);
    else window.history.pushState(null, "", href);
    setState(readLocation());
  }, []);

  useEffect(() => {
    const onPop = () => setState(readLocation());
    window.addEventListener("popstate", onPop);
    return () => window.removeEventListener("popstate", onPop);
  }, []);

  const tz = resolveTimeZone(
    state.filters.tz,
    readStoredZone(),
    Intl.DateTimeFormat().resolvedOptions().timeZone,
  );
  useEffect(() => {
    if (state.filters.tz === tz) return;
    const search = new URLSearchParams(window.location.search);
    search.set("tz", tz);
    navigate(window.location.pathname + "?" + search, true);
  }, [state, tz, navigate]);

  const eff = useMemo(
    () => effectiveFilters(state.filters, tz),
    [state.filters, tz],
  );
  const makeHref = useCallback<PageProps["makeHref"]>(
    (page, options) =>
      buildHref({
        page,
        key: options?.key ?? "",
        id: options?.id ?? "",
        filters: { ...state.filters, tz, ...options?.patch },
      }),
    [state.filters, tz],
  );

  return { state, eff, navigate, makeHref, tz };
}
