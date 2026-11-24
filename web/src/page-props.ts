import type { Filters, Page, UrlState } from "./url.ts";
export interface PageProps {
  state: UrlState;
  eff: Filters;
  navigate: (href: string, replace?: boolean) => void;
  makeHref: (
    page: Page,
    opts?: { key?: string; id?: string; patch?: Partial<Filters> },
  ) => string;
  tz: string;
}
