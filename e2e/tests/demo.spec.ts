import { join } from "node:path";
import {
  expect,
  test as base,
  type Page,
  type TestInfo,
} from "@playwright/test";

const test = base.extend({
  page: async ({ page }, use) => {
    const errors: string[] = [];
    await page.exposeFunction(
      "recordCspViolation",
      (blocked: string, directive: string) => {
        errors.push(`CSP violation: ${directive} (${blocked})`);
      },
    );
    await page.addInitScript(() => {
      window.addEventListener("securitypolicyviolation", (event) => {
        void (
          window as Window & {
            recordCspViolation: (
              blocked: string,
              directive: string,
            ) => Promise<void>;
          }
        ).recordCspViolation(event.blockedURI, event.violatedDirective);
      });
    });
    page.on("pageerror", (error) => errors.push(error.message));
    page.on("console", (message) => {
      if (
        message.type() !== "error" &&
        !message.text().includes("Content Security Policy")
      )
        return;
      const url = message.location().url;
      // Chromium logs the intentional demo 503 independently of how the app handles it.
      if (
        url.endsWith("/api/fetch") &&
        message.text().startsWith("Failed to load resource:") &&
        message.text().includes("503")
      )
        return;
      errors.push(message.text());
    });
    await use(page);
    expect(errors).toEqual([]);
  },
});

async function checkPage(page: Page, info: TestInfo, name: string) {
  const width = await page.evaluate(() => ({
    viewport: document.documentElement.clientWidth,
    document: document.documentElement.scrollWidth,
    body: document.body.scrollWidth,
  }));
  expect(width.document).toBeLessThanOrEqual(width.viewport);
  expect(width.body).toBeLessThanOrEqual(width.viewport);
  if (process.env.RUA_SCREENSHOTS) {
    await page.screenshot({
      path: join(
        process.env.RUA_SCREENSHOTS,
        `${name}-${info.project.name}.png`,
      ),
      fullPage: name !== "reports",
    });
  }
}

function messageTotal(page: Page) {
  return page.getByTestId("stat-value-messages");
}

async function numberOfMessages(page: Page) {
  return Number((await messageTotal(page).textContent())?.replaceAll(",", ""));
}

async function expectUrlParam(page: Page, key: string, value: string | null) {
  await expect
    .poll(() => new URL(page.url()).searchParams.get(key))
    .toBe(value);
}

test("overview, policy markers, date range, domain and time zone", async ({
  page,
}, info) => {
  const landing = await page.goto("/");
  expect(landing?.headers()["content-security-policy"]).toContain(
    "default-src 'self'",
  );
  await expect(page.getByTestId("overview-heading")).toBeVisible();
  await expectUrlParam(page, "tz", "UTC");
  await expect(messageTotal(page)).toHaveText(/^[1-9][\d,]*$/);
  await expect(page.getByTestId("stat-value-pass-rate")).toHaveText(
    /^[\d.]+%$/,
  );
  for (const id of [
    "forwarded",
    "failing",
    "reports",
    "quarantined",
    "rejected",
  ]) {
    await expect(page.getByTestId(`stat-value-${id}`)).toHaveText(
      /^[1-9][\d,]*$/,
    );
  }

  await expect(
    page.getByRole("group", { name: "Daily messages by DMARC verdict" }),
  ).toBeVisible();
  const marker = page
    .getByTestId("policy-marker")
    .and(page.getByRole("img", { name: /Policy changes.*example\.net/ }))
    .first();
  await expect(marker).toBeVisible();
  await expect(
    page
      .getByTestId("new-unknown-sources")
      .getByRole("link", { name: "Example Transit" }),
  ).toBeVisible();
  await checkPage(page, info, "overview");

  await marker.focus();
  await expect(page.getByRole("tooltip")).toContainText("example.net");
  await page.keyboard.press("Escape");
  await expect(page.getByRole("tooltip")).toHaveCount(0);
  await page.getByTestId("range-picker").focus();
  if (!info.project.use.isMobile) {
    await marker.locator("rect").hover();
    await expect(page.getByRole("tooltip")).toContainText("example.net");
  }

  const total = await numberOfMessages(page);
  await page.getByTestId("domain-picker").selectOption("example.net");
  await expectUrlParam(page, "domain", "example.net");
  await expect.poll(() => numberOfMessages(page)).toBeLessThan(total);
  const domainTotal = await numberOfMessages(page);
  expect(domainTotal).toBeGreaterThan(0);

  await page.getByTestId("range-picker").selectOption("7");
  await expectUrlParam(page, "range", "7");
  await expect.poll(() => numberOfMessages(page)).toBeLessThan(domainTotal);

  const overviewResponse = page.waitForResponse(
    (response) =>
      response.url().includes("/api/overview?") &&
      new URL(response.url()).searchParams.get("tz") === "America/New_York",
  );
  await page.getByTestId("tz-picker").selectOption("America/New_York");
  const overview = await (await overviewResponse).json();
  await expectUrlParam(page, "tz", "America/New_York");
  await expect(messageTotal(page)).toHaveText(
    new Intl.NumberFormat("en-GB").format(overview.totals.messages),
  );
  await expect(page.getByTestId("range-label")).toContainText(
    `${overview.range.from} to ${overview.range.to}`,
  );

  await page.getByTestId("nav-sources").click();
  await expect(page.getByTestId("sources-table")).toBeVisible();
  await expectUrlParam(page, "domain", "example.net");
  await expectUrlParam(page, "range", "7");
  await expectUrlParam(page, "tz", "America/New_York");
  await page.goBack();
  await expect(page.getByTestId("overview-heading")).toBeVisible();
  await expect(page.getByTestId("domain-picker")).toHaveValue("example.net");
  await expect(page.getByTestId("range-picker")).toHaveValue("7");
  await expect(page.getByTestId("tz-picker")).toHaveValue("America/New_York");

  const presetMessages = await numberOfMessages(page);
  await page.getByTestId("range-picker").selectOption("custom");
  await expectUrlParam(page, "range", "custom");
  await expect(page.getByTestId("range-from")).toHaveValue(overview.range.from);
  await expect(page.getByTestId("range-to")).toHaveValue(overview.range.to);
  await page.getByTestId("range-from").fill(overview.range.to);
  await expectUrlParam(page, "from", overview.range.to);
  await expect(page.getByTestId("range-label")).toContainText(
    `${overview.range.to} to ${overview.range.to}`,
  );
  await expect.poll(() => numberOfMessages(page)).toBeLessThan(presetMessages);

  await page.goto("/");
  await expectUrlParam(page, "tz", "America/New_York");
  await page.goto("/?tz=UTC");
  await expect(page.getByTestId("tz-picker")).toHaveValue("UTC");
});

test("sources can be sorted, filtered and opened", async ({ page }, info) => {
  await page.goto("/sources");
  const table = page.getByTestId("sources-table");
  await expect(table).toBeVisible();
  await checkPage(page, info, "sources");
  const count = await table.locator("tbody tr").count();
  await page.getByTestId("unknown-only-toggle").check();
  await expectUrlParam(page, "unknown", "1");
  await expect(table.getByTestId("unknown-source").first()).toBeVisible();
  const unknowns = await table.locator("tbody tr").count();
  expect(unknowns).toBeGreaterThan(1);
  expect(unknowns).toBeLessThan(count);
  await expect(table.getByTestId("unknown-source")).toHaveCount(unknowns);

  await table.getByRole("button", { name: "Sort by Messages" }).click();
  await expect(table.locator('th[aria-sort="ascending"]')).toContainText(
    "Messages",
  );
  const volumes = (
    await table.locator("tbody td:nth-child(2)").allTextContents()
  ).map((value) => Number(value.replaceAll(",", "")));
  expect(volumes).toEqual([...volumes].sort((a, b) => a - b));

  await page.getByTestId("sources-filter").fill("Example Transit");
  await expectUrlParam(page, "q", "Example Transit");
  await expect(table.locator("tbody tr")).toHaveCount(1);
  await table.getByRole("link", { name: "Example Transit" }).click();
  await expect(page.getByTestId("source-detail-heading")).toHaveText(
    "Example Transit",
  );
  await expect(
    page.getByTestId("source-ips-table").locator("tbody tr").first(),
  ).toBeVisible();
  await expect(
    page.getByTestId("combinations-table").locator("tbody tr").first(),
  ).toBeVisible();
  await expect(
    page.getByRole("group", { name: "Daily messages by DMARC verdict" }),
  ).toBeVisible();
  await checkPage(page, info, "source-detail");
  await page.goBack();
  await expect(page.getByTestId("unknown-only-toggle")).toBeChecked();
  await expect(page.getByTestId("sources-filter")).toHaveValue(
    "Example Transit",
  );
});

test("reports paginate and expose the original downloads", async ({
  page,
  request,
}, info) => {
  await page.goto("/reports?kind=dmarc-aggregate&tz=Europe%2FIstanbul");
  const table = page.getByTestId("reports-table");
  const rows = table.locator("tbody tr");
  await expect(rows.first()).toBeVisible();
  await checkPage(page, info, "reports");
  const firstCount = await rows.count();
  const loadMore = page.getByTestId("reports-load-more");
  await loadMore.click();
  await expect.poll(() => rows.count()).toBeGreaterThan(firstCount);
  const hrefs = await table
    .locator("tbody td:first-child a")
    .evaluateAll((links) => links.map((link) => link.getAttribute("href")));
  expect(new Set(hrefs).size).toBe(hrefs.length);
  await rows.first().getByRole("link").click();
  await expect(page.getByTestId("report-detail-heading")).toHaveText(
    /^Report #\d+$/,
  );
  await expect(
    page.getByTestId("report-records-table").locator("tbody tr").first(),
  ).toBeVisible();
  for (const [id, route] of [
    ["report-raw-download", "reports"],
    ["message-raw-download", "messages"],
  ]) {
    const link = page.getByTestId(id);
    await expect(link).toHaveAttribute(
      "href",
      new RegExp(`^/api/${route}/\\d+/raw$`),
    );
    await expect(link).toHaveAttribute("download", "");
    const href = await link.getAttribute("href");
    const raw = await request.get(href!);
    expect(raw.ok()).toBe(true);
    expect(raw.headers()["content-disposition"]).toMatch(/^attachment;/);
    expect((await raw.body()).length).toBeGreaterThan(0);
  }
  await checkPage(page, info, "report-detail");

  await page.getByTestId("nav-reports").click();
  await page.getByTestId("reports-kind-filter").selectOption("tls-report");
  await expectUrlParam(page, "kind", "tls-report");
  const tlsRow = rows.filter({ hasText: /TLS ·/ }).first();
  await expect(tlsRow).toBeVisible();
  await tlsRow.getByRole("link").click();
  await expect(
    page.getByTestId("report-tls-policies-table").locator("tbody tr").first(),
  ).toBeVisible();
  await checkPage(page, info, "report-tls-detail");
});

test("TLS sessions, failure details and policies", async ({ page }, info) => {
  await page.goto("/tls");
  const domains = page.getByTestId("tls-domains-table");
  await expect(domains).toBeVisible();
  await checkPage(page, info, "tls");
  await domains.getByRole("link", { name: "example.com" }).click();
  await expect(page.getByTestId("tls-domain-heading")).toHaveText(
    "example.com",
  );
  await expect(
    page.getByRole("group", { name: "Daily TLS sessions by outcome" }),
  ).toBeVisible();
  await expect(
    page.getByTestId("tls-failures-table").locator("tbody tr").first(),
  ).toBeVisible();
  await expect(
    page.getByTestId("tls-policies-table").locator("tbody tr").first(),
  ).toBeVisible();
  await checkPage(page, info, "tls-detail");
});

test("status shows ingestion data and handles disabled fetch", async ({
  page,
}, info) => {
  await page.goto("/status?tz=America%2FNew_York");
  await expect(page.getByTestId("status-heading")).toBeVisible();
  for (const id of [
    "classes-table",
    "problems-table",
    "other-messages-table",
    "failure-reports-table",
  ]) {
    await expect(
      page.getByTestId(id).locator("tbody tr").first(),
    ).toBeVisible();
  }
  await checkPage(page, info, "status");
  const fetchResponse = page.waitForResponse((response) =>
    response.url().endsWith("/api/fetch"),
  );
  await page.getByTestId("fetch-now").click();
  const response = await fetchResponse;
  expect(response.request().method()).toBe("POST");
  expect(response.request().headers()["x-rua"]).toBe("1");
  expect(response.status()).toBe(503);
  expect((await response.json()).error.code).toBe("fetch_disabled");
  await expect(page.getByTestId("fetch-message")).toHaveText(
    "Fetching is disabled (IMAP not configured).",
  );
});
