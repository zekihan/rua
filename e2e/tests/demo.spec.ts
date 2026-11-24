import { join } from "node:path";
import {
  expect,
  test as base,
  type Locator,
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

function isFocused(locator: Locator) {
  return locator.evaluate((node) => node === document.activeElement);
}

function isPhoneViewport(page: Page) {
  return (page.viewportSize()?.width ?? 1280) < 640;
}

function isPhone(info: TestInfo) {
  return Boolean(info.project.use.isMobile);
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

test("daily chart bars are inspectable and the chart has a table", async ({
  page,
}, info) => {
  await page.goto("/");
  const chart = page
    .getByTestId("daily-chart")
    .filter({
      has: page.getByRole("group", { name: "Daily messages by DMARC verdict" }),
    })
    .first();
  const days = chart.getByTestId("chart-day");
  await expect(days.first()).toBeVisible();
  const dayCount = await days.count();
  expect(dayCount).toBeGreaterThan(6);
  await expect(days.nth(3)).toHaveAttribute(
    "aria-label",
    /^\d{4}-\d{2}-\d{2}: Pass [\d,]+, Forwarded [\d,]+, Fail [\d,]+$/,
  );

  await expect(days.locator("xpath=self::*[@tabindex='0']")).toHaveCount(0);
  await expect(chart.getByTestId("daily-chart-group")).toHaveAttribute(
    "tabindex",
    "0",
  );

  const group = chart.getByTestId("daily-chart-group");
  const toggle = chart.getByTestId("chart-table-toggle");
  await page.getByTestId("range-picker").focus();
  for (let i = 0; i < 40 && !(await isFocused(group)); i++)
    await page.keyboard.press("Tab");
  await expect(group).toBeFocused();
  const tooltip = chart.getByRole("tooltip");
  const dayLabelAt = async (index: number) =>
    (await days.nth(index).getAttribute("aria-label")) ?? "";
  await expect(tooltip).toContainText(
    (await dayLabelAt(dayCount - 1)).split(": ")[0],
  );
  await page.keyboard.press("ArrowLeft");
  await expect(tooltip).toContainText(
    (await dayLabelAt(dayCount - 2)).split(": ")[0],
  );
  await page.keyboard.press("Home");
  await expect(tooltip).toContainText((await dayLabelAt(0)).split(": ")[0]);
  await page.keyboard.press("ArrowLeft");
  await expect(tooltip).toContainText((await dayLabelAt(0)).split(": ")[0]);
  for (let i = 0; i < 3; i++) await page.keyboard.press("ArrowRight");
  const label = await dayLabelAt(3);
  const [date, counts] = label.split(": ");
  await expect(tooltip).toContainText(date);
  await expect(tooltip).toContainText(counts.split(", ")[2]);
  await expect(chart.getByTestId("chart-live")).toHaveText(label);
  await page.keyboard.press("End");
  await expect(tooltip).toContainText(
    (await dayLabelAt(dayCount - 1)).split(": ")[0],
  );
  await page.keyboard.press("Escape");
  await expect(tooltip).toHaveCount(0);
  await page.keyboard.press("Tab");
  await expect(toggle.getByText("Show as table")).toBeFocused();

  if (isPhone(info)) {
    await days.nth(5).tap();
    await expect(tooltip).toContainText(
      ((await days.nth(5).getAttribute("aria-label")) ?? "").split(": ")[0],
    );
    await page.getByTestId("overview-heading").tap();
    await expect(tooltip).toHaveCount(0);
  }

  await expect(chart.getByTestId("chart-table")).toBeHidden();
  await toggle.getByText("Show as table").click();
  const table = chart.getByTestId("chart-table");
  await expect(table).toBeVisible();
  await expect(table.locator("thead th")).toHaveText([
    "Date",
    "Pass",
    "Forwarded",
    "Fail",
  ]);
  await expect(table.locator("tbody tr")).toHaveCount(dayCount);
  await expect(table.locator("tbody tr").nth(3)).toContainText(date);
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
  const phone = isPhone(info);
  const rows = phone
    ? page.getByTestId("report-card")
    : page.getByTestId("reports-table").locator("tbody tr");
  const rowLinks = phone ? rows.locator("a") : rows.locator("td:first-child a");
  await expect(page.getByTestId("reports-verdict-filter")).toHaveCount(0);
  await expect(rows.first()).toBeVisible();
  await checkPage(page, info, "reports");
  const firstCount = await rows.count();
  const loadMore = page.getByTestId("reports-load-more");
  await loadMore.click();
  await expect.poll(() => rows.count()).toBeGreaterThan(firstCount);
  const hrefs = await rowLinks.evaluateAll((links) =>
    links.map((link) => link.getAttribute("href")),
  );
  expect(new Set(hrefs).size).toBe(hrefs.length);
  if (phone) {
    const card = rows.first();
    await expect(card).toContainText(/Messages [\d,]+/);
    await expect(card.getByTestId("report-card-window")).toHaveText(
      /^\d{1,2} \w{3} \d{2}:\d{2} – (\d{1,2} \w{3} )?\d{2}:\d{2}$/,
    );
    await expect(card).toContainText(/Pass rate [\d.]+%/);
    await expect(card).toContainText(/example\.(com|net|org)/);
    await expect(card).toContainText(/\d{4}-\d{2}-\d{2}/);
  }
  const reportsUrl = page.url();
  await rows.first().getByRole("link").click();
  await expect(page.getByTestId("report-detail-heading")).toHaveText(
    /^Report #\d+$/,
  );
  await expect(page.getByTestId("report-detail-heading")).toBeFocused();
  const record = phone
    ? page.getByTestId("report-record-card").first()
    : page.getByTestId("report-records-table").locator("tbody tr").first();
  await expect(record).toBeVisible();
  await expect(record).toContainText(/Pass|Forwarded|Fail/);
  await expect(record).toContainText(/none|quarantine|reject/);
  await expect(record).toContainText(/\d+\.\d+\.\d+\.\d+|:/);
  if (phone) {
    const auth = page.getByTestId("record-auth-details").first();
    await expect(auth).not.toHaveAttribute("open", "");
    await auth.locator("summary").click();
    await expect(auth).toContainText(/=\s*(pass|fail|none|neutral)/);
  }
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

  await page.goBack();
  expect(page.url()).toBe(reportsUrl);
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

test("reports list the receivers that report and filter by one", async ({
  page,
}) => {
  await page.goto("/reports");
  const list = page.getByTestId("receivers-list");
  await expect(list).toBeVisible();
  const first = page.getByTestId("receiver-row").first();
  await expect(first).toContainText(/\d+ reports? · [\d,]+ messages · last /);
  const org = (await first.getByRole("link").textContent()) ?? "";
  await first.getByRole("link").click();
  await expectUrlParam(page, "reporter", org);
  await expect(page.getByTestId("reports-reporter-filter")).toHaveValue(org);
  const rows = isPhoneViewport(page)
    ? page.getByTestId("report-card")
    : page.getByTestId("reports-table").locator("tbody tr");
  await expect(rows.first()).toContainText(org);
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
  const failed = page.getByRole("group", {
    name: "Daily failed TLS sessions",
  });
  await expect(failed).toBeVisible();
  const failedChart = page.getByTestId("daily-chart").filter({ has: failed });
  await failedChart.getByText("Show as table").click();
  await expect(failedChart.getByTestId("chart-table")).toBeVisible();
  await expect(
    failedChart.getByTestId("chart-table").locator("thead th"),
  ).toHaveText(["Date", "Failed"]);
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
  const phone = isPhone(info);
  for (const id of ["classes-table", "failure-reports-table"]) {
    await expect(
      page.getByTestId(id).locator("tbody tr").first(),
    ).toBeVisible();
  }
  for (const id of ["problems-table", "other-messages-table"]) {
    await expect(
      phone
        ? page.getByTestId(`${id}-cards`).getByTestId("message-card").first()
        : page.getByTestId(id).locator("tbody tr").first(),
    ).toBeVisible();
  }
  const problem = phone
    ? page
        .getByTestId("problems-table-cards")
        .getByTestId("message-card")
        .filter({ has: page.getByTestId("message-error") })
        .first()
    : page.getByTestId("problems-table").locator("tbody tr").first();
  await expect(problem).toBeVisible();
  if (phone) await expect(problem.getByTestId("message-error")).not.toBeEmpty();
  if (!phone) {
    const mailbox = page.getByTestId("mailboxes-table");
    const scroller = mailbox.locator("xpath=..");
    expect(
      await scroller.evaluate((node) => node.scrollWidth - node.clientWidth),
    ).toBeLessThanOrEqual(0);
    await expect(mailbox.locator("thead th")).toHaveText([
      "Name",
      "UIDVALIDITY",
      "Next UID",
      "Pending",
      "Last fetch",
      "Last success",
      "Result",
    ]);
  }
  await checkPage(page, info, "status");
  const fetchNow = page.getByTestId("fetch-now");
  await expect(fetchNow).toBeDisabled();
  await expect(fetchNow).toHaveAccessibleDescription(/IMAP is not configured/);
  await expect(page.getByTestId("fetch-disabled-note")).toBeVisible();
});

test("status shows the pending backlog per mailbox", async ({ page }, info) => {
  await page.goto("/status");
  const cells = page.getByTestId(
    isPhone(info) ? "mailbox-card-pending" : "mailbox-pending",
  );
  await expect(cells.first()).toBeVisible();
  for (const text of await cells.allTextContents())
    expect(text.trim()).toMatch(/(—|[\d,]+)$/);
});

test("navigation moves focus to the page heading", async ({ page }) => {
  await page.goto("/");
  await page.getByTestId("nav-status").click();
  await expect(page.getByTestId("status-heading")).toBeFocused();
  await page.getByTestId("nav-sources").click();
  await expect(page.getByRole("heading", { level: 1 })).toBeFocused();
});

test("form controls have a visible border", async ({ page }) => {
  await page.goto("/reports");
  const select = page.getByTestId("reports-kind-filter");
  await expect(select).toBeVisible();
  const [border, fill] = await select.evaluate((element) => {
    const style = getComputedStyle(element);
    return [style.borderTopColor, style.backgroundColor];
  });
  const luminance = (css: string) => {
    const [r, g, b] = (css.match(/[\d.]+/g) ?? []).slice(0, 3).map((value) => {
      const channel = Number(value) / 255;
      return channel <= 0.03928
        ? channel / 12.92
        : ((channel + 0.055) / 1.055) ** 2.4;
    });
    return 0.2126 * r + 0.7152 * g + 0.0722 * b;
  };
  const [a, b] = [luminance(border), luminance(fill)].sort((x, y) => y - x);
  expect((a + 0.05) / (b + 0.05)).toBeGreaterThanOrEqual(3);
});
