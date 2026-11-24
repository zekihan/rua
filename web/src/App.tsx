import { useEffect, type MouseEvent } from "react";
import { AppHeader } from "./components/AppHeader.tsx";
import { PageHeading } from "./components/ui.tsx";
import { useNavigation } from "./hooks/useNavigation.ts";
import Overview from "./pages/Overview.tsx";
import Sources from "./pages/Sources.tsx";
import SourceDetail from "./pages/SourceDetail.tsx";
import Reports from "./pages/Reports.tsx";
import ReportDetail from "./pages/ReportDetail.tsx";
import { TlsList, TlsDomain } from "./pages/Tls.tsx";
import StatusPage from "./pages/Status.tsx";

const pages = {
  overview: Overview,
  sources: Sources,
  source: SourceDetail,
  reports: Reports,
  report: ReportDetail,
  tls: TlsList,
  tlsDomain: TlsDomain,
  status: StatusPage,
};

export default function App() {
  const props = useNavigation();
  const { page, key, id } = props.state;
  const Page = page === "notFound" ? null : pages[page];

  useEffect(() => {
    window.scrollTo(0, 0);
  }, [page, key, id]);

  function followLink(event: MouseEvent<HTMLDivElement>) {
    if (
      event.defaultPrevented ||
      event.button !== 0 ||
      event.metaKey ||
      event.ctrlKey ||
      event.shiftKey ||
      event.altKey ||
      !(event.target instanceof Element)
    )
      return;
    const link = event.target.closest("a");
    if (
      !link ||
      link.target ||
      link.hasAttribute("download") ||
      link.hash ||
      link.origin !== window.location.origin ||
      link.pathname.startsWith("/api/")
    )
      return;
    event.preventDefault();
    props.navigate(link.pathname + link.search);
  }

  return (
    <div onClick={followLink}>
      <a
        href="#main"
        className="sr-only focus:not-sr-only focus:absolute focus:z-50 focus:bg-surface focus:p-3"
      >
        Skip to content
      </a>
      <AppHeader {...props} />
      <main id="main" tabIndex={-1} className="shell py-5 sm:py-7">
        {Page ? (
          <Page {...props} />
        ) : (
          <div className="space-y-3">
            <PageHeading>Page not found</PageHeading>
            <a className="link" href={props.makeHref("overview")}>
              Back to overview
            </a>
          </div>
        )}
      </main>
    </div>
  );
}
