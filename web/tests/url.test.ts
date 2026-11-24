import { test } from "node:test";
import assert from "node:assert/strict";
import { parseUrl, buildHref, resolveTimeZone, computeRange, dateInTimeZone, addDays } from "../src/url.ts";

test("parse/build round-trip for every page", () => {
  const cases: [string, string][] = [
    ["/", ""], ["/sources", "?domain=example.com&tz=Europe%2FIstanbul&unknown=1"],
    ["/sources/a%2Fb%20c", "?tz=UTC"], ["/reports", "?reporter=Google&kind=tls-report&verdict=fail"],
    ["/reports/42", "?tz=UTC"], ["/tls", ""], ["/tls/example.com", "?from=2026-01-01&to=2026-01-31&range=custom&tz=UTC"], ["/status", "?tz=UTC"],
  ];
  for (const [path, search] of cases) {
    const s = parseUrl(path, search);
    const href = buildHref(s);
    const s2 = parseUrl(...(href.split("?") as [string, string]) && (href.includes("?") ? [href.slice(0, href.indexOf("?")), href.slice(href.indexOf("?"))] as [string, string] : [href, ""] as [string, string]));
    assert.equal(s2.page, s.page); assert.deepEqual(s2.filters, s.filters); assert.equal(s2.key, s.key); assert.equal(s2.id, s.id);
  }
});

test("encoded source key survives", () => {
  const s = parseUrl("/sources/a%2Fb%20c", "");
  assert.equal(s.key, "a/b c");
  assert.equal(buildHref(s), "/sources/a%2Fb%20c");
});

test("tz resolution order: URL > stored > browser > UTC", () => {
  assert.equal(resolveTimeZone("Europe/Istanbul", "UTC", "America/New_York"), "Europe/Istanbul");
  assert.equal(resolveTimeZone("", "Europe/Istanbul", "America/New_York"), "Europe/Istanbul");
  assert.equal(resolveTimeZone("", null, "America/New_York"), "America/New_York");
  assert.equal(resolveTimeZone("", null, ""), "UTC");
});

test("presets: 7 days is today inclusive", () => {
  const now = new Date("2026-03-15T12:00:00Z");
  assert.deepEqual(computeRange("7", "UTC", now), { from: "2026-03-09", to: "2026-03-15" });
  assert.deepEqual(computeRange("30", "UTC", now), { from: "2026-02-14", to: "2026-03-15" });
});

test("range across DST spring-forward America/New_York", () => {
  // DST starts 2026-03-08; 02:30 local does not exist.
  const now = new Date("2026-03-09T06:30:00Z"); // 01:30 EST? after transition it's EDT 02:30 -> actually 06:30Z = 02:30 EDT
  assert.equal(dateInTimeZone("America/New_York", now), "2026-03-09");
  assert.deepEqual(computeRange("7", "America/New_York", now), { from: "2026-03-03", to: "2026-03-09" });
  const before = new Date("2026-03-08T04:00:00Z"); // 23:00 EST previous day
  assert.equal(dateInTimeZone("America/New_York", before), "2026-03-07");
});

test("range across DST in Europe/Istanbul (no DST, UTC+3)", () => {
  const now = new Date("2026-10-25T21:30:00Z"); // 2026-10-26 00:30 in Istanbul
  assert.equal(dateInTimeZone("Europe/Istanbul", now), "2026-10-26");
  assert.deepEqual(computeRange("7", "Europe/Istanbul", now), { from: "2026-10-20", to: "2026-10-26" });
  assert.deepEqual(computeRange("365", "Europe/Istanbul", now).to, "2026-10-26");
});

test("custom range passes through; addDays across month/year", () => {
  assert.deepEqual(computeRange("custom", "UTC", new Date("2026-01-01T00:00:00Z"), "2025-12-01", "2025-12-31"), { from: "2025-12-01", to: "2025-12-31" });
  assert.equal(addDays("2026-12-31", 1), "2027-01-01");
  assert.equal(addDays("2026-03-01", -1), "2026-02-28");
});
