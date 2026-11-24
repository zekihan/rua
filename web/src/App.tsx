import { useCallback, useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { parseUrl, buildHref, resolveTimeZone, effectiveFilters, supportedTimeZones, type Filters, type Page, type UrlState } from "./url.ts";
import { api } from "./api/client.ts";
import type { PageProps } from "./page-props.ts";
import Overview from "./pages/Overview.tsx";
import Sources from "./pages/Sources.tsx";
import SourceDetail from "./pages/SourceDetail.tsx";
import Reports from "./pages/Reports.tsx";
import ReportDetail from "./pages/ReportDetail.tsx";
import { TlsList, TlsDomain } from "./pages/Tls.tsx";
import StatusPage from "./pages/Status.tsx";

const TZ_KEY = "rua.tz";

export default function App() {
  const [state, setState] = useState<UrlState>(() => parseUrl(window.location.pathname, window.location.search));
  const navigate = useCallback((href: string, replace = false) => {
    if (replace) window.history.replaceState(null, "", href); else window.history.pushState(null, "", href);
    setState(parseUrl(window.location.pathname, window.location.search));
  }, []);
  useEffect(() => {
    const onPop = () => setState(parseUrl(window.location.pathname, window.location.search));
    window.addEventListener("popstate", onPop); return () => window.removeEventListener("popstate", onPop);
  }, []);
  // TZ resolution: URL -> localStorage -> browser -> UTC; write into URL with replaceState.
  const browserTz = useMemo(() => { try { return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC"; } catch { return "UTC"; } }, []);
  const storedTz = useMemo(() => { try { return window.localStorage.getItem(TZ_KEY); } catch { return null; } }, []);
  const tz = resolveTimeZone(state.filters.tz, storedTz, browserTz);
  useEffect(() => {
    if (!state.filters.tz) {
      const href = buildHref({ ...state, filters: { ...state.filters, tz } });
      window.history.replaceState(null, "", href);
      setState(parseUrl(window.location.pathname, window.location.search));
    }
  }, [state, tz]);
  const eff = useMemo(() => effectiveFilters(state.filters, tz), [state.filters, tz]);
  const makeHref = useCallback((page: Page, opts?: { key?: string; id?: string; patch?: Partial<Filters> }) =>
    buildHref({ page, key: opts?.key ?? "", id: opts?.id ?? "", filters: { ...state.filters, tz, ...(opts?.patch ?? {}) } }), [state.filters, tz]);

  const props: PageProps = { state, eff, navigate, makeHref, tz };
  const domainsQ = useQuery({ queryKey: ["domains"], queryFn: () => api.domains() });
  const zones = useMemo(() => { const z = supportedTimeZones(); return z.includes(tz) ? z : [tz, ...z]; }, [tz]);
  const onNav = (e: React.MouseEvent<HTMLAnchorElement>, href: string) => { if (e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return; e.preventDefault(); navigate(href); };
  const navLink = (page: Page, label: string, testid: string) => { const href = makeHref(page); return <a href={href} data-testid={testid} aria-current={state.page === page ? "page" : undefined} onClick={(e) => onNav(e, href)} style={{ color: "var(--ink)", fontWeight: state.page === page ? 700 : 400 }}>{label}</a>; };
  const aggregating = ["overview", "sources", "source", "reports", "tls", "tlsDomain"].includes(state.page);

  return (
    <div>
      <a href="#main" className="skip-link">Skip to content</a>
      <header style={{ borderBottom: "1px solid var(--line)", background: "var(--surface)", position: "sticky", top: 0, zIndex: 10 }}>
        <div style={{ maxWidth: 1100, margin: "0 auto", padding: "10px 14px", display: "flex", gap: 14, flexWrap: "wrap", alignItems: "center" }}>
          <strong style={{ fontSize: "1.1rem" }}>rua</strong>
          <nav aria-label="Primary" style={{ display: "flex", gap: 12, flexWrap: "wrap" }}>
            {navLink("overview", "Overview", "nav-overview")}{navLink("sources", "Sources", "nav-sources")}{navLink("reports", "Reports", "nav-reports")}{navLink("tls", "TLS", "nav-tls")}{navLink("status", "Status", "nav-status")}
          </nav>
          <label style={{ marginLeft: "auto" }} className="small">Time zone{" "}
            <select className="input" data-testid="tz-picker" aria-label="Time zone" value={tz} onChange={(e) => { try { window.localStorage.setItem(TZ_KEY, e.target.value); } catch { /* ignore */ } navigate(makeHref(state.page, { key: state.key, id: state.id, patch: { tz: e.target.value } })); }}>
              {zones.map((z) => <option key={z} value={z}>{z}</option>)}
            </select>
          </label>
        </div>
        {aggregating && (
          <div style={{ maxWidth: 1100, margin: "0 auto", padding: "0 14px 10px", display: "flex", gap: 10, flexWrap: "wrap", alignItems: "end" }}>
            <label className="small">Domain{" "}
              <select className="input" data-testid="domain-picker" aria-label="Domain" value={state.filters.domain} onChange={(e) => navigate(makeHref(state.page, { key: state.key, id: state.id, patch: { domain: e.target.value } }))}>
                <option value="">All domains</option>
                {(domainsQ.data?.domains ?? []).map((d) => <option key={d.domain} value={d.domain}>{d.domain}</option>)}
              </select></label>
            <label className="small">Range{" "}
              <select className="input" data-testid="range-picker" aria-label="Range" value={state.filters.range} onChange={(e) => navigate(makeHref(state.page, { key: state.key, id: state.id, patch: { range: e.target.value } }))}>
                <option value="7">7 days</option><option value="30">30 days</option><option value="90">90 days</option><option value="365">365 days</option><option value="custom">Custom</option>
              </select></label>
            {state.filters.range === "custom" && <>
              <label className="small">From <input type="date" className="input" data-testid="range-from" aria-label="From date" value={eff.from} onChange={(e) => navigate(makeHref(state.page, { key: state.key, id: state.id, patch: { from: e.target.value } }))} /></label>
              <label className="small">To <input type="date" className="input" data-testid="range-to" aria-label="To date" value={eff.to} onChange={(e) => navigate(makeHref(state.page, { key: state.key, id: state.id, patch: { to: e.target.value } }))} /></label>
            </>}
            <span className="small muted" data-testid="range-label">{eff.from} to {eff.to} ({tz})</span>
          </div>
        )}
      </header>
      <main id="main" style={{ maxWidth: 1100, margin: "0 auto", padding: 14 }}>
        {state.page === "overview" && <Overview {...props} />}
        {state.page === "sources" && <Sources {...props} />}
        {state.page === "source" && <SourceDetail {...props} />}
        {state.page === "reports" && <Reports {...props} />}
        {state.page === "report" && <ReportDetail {...props} />}
        {state.page === "tls" && <TlsList {...props} />}
        {state.page === "tlsDomain" && <TlsDomain {...props} />}
        {state.page === "status" && <StatusPage {...props} />}
        {state.page === "notFound" && <><h1>Not found</h1><p><a href={makeHref("overview")} onClick={(e) => onNav(e, makeHref("overview"))}>Back to overview</a></p></>}
      </main>
    </div>
  );
}
