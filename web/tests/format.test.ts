import { test } from "node:test";
import assert from "node:assert/strict";
import {
  formatNumber,
  formatPercent,
  formatBytes,
  formatDateLabel,
  formatUnixDate,
  formatUnixDateTime,
  formatUnixWindow,
  formatPending,
} from "../src/format.ts";
test("numbers and percents", () => {
  assert.equal(formatNumber(1234567), "1,234,567");
  assert.equal(formatPercent(0.956), "95.6%");
  assert.equal(formatPercent(1), "100.0%");
  assert.equal(formatPercent(null), "—");
  assert.equal(formatBytes(512), "512 B");
  assert.equal(formatBytes(2048), "2.0 KiB");
});

test("dates formatted in selected tz", () => {
  const ts = Date.UTC(2026, 0, 1, 22, 0) / 1000; // 22:00 UTC = next day in Istanbul
  assert.match(formatUnixDate(ts, "Europe/Istanbul"), /2 Jan 2026/);
  assert.match(formatUnixDate(ts, "UTC"), /1 Jan 2026/);
  assert.match(formatUnixDateTime(ts, "UTC"), /22:00/);
  assert.equal(formatUnixDate(null, "UTC"), "—");
  assert.equal(formatUnixDate(0, "UTC"), "—");
});

test("calendar dates stay on their reported day and timestamps follow DST", () => {
  assert.equal(formatDateLabel("2026-03-08"), "8 Mar 2026");

  const before = Date.parse("2026-03-08T06:59:00Z") / 1000;
  const after = Date.parse("2026-03-08T07:01:00Z") / 1000;
  assert.match(formatUnixDateTime(before, "America/New_York"), /01:59/);
  assert.match(formatUnixDateTime(after, "America/New_York"), /03:01/);
  assert.equal(formatUnixDateTime(null, "UTC"), "—");
});

test("zero differs from a missing rate, and invalid byte counts are hidden", () => {
  assert.equal(formatPercent(0), "0.0%");
  assert.equal(formatPercent(Number.NaN), "—");
  assert.equal(formatBytes(-1), "—");
  assert.equal(formatBytes(1024 ** 3), "1.0 GiB");
});

test("pending backlog shows a dash when unknown", () => {
  assert.equal(formatPending(null), "—");
  assert.equal(formatPending(0), "0");
  assert.equal(formatPending(1500), "1,500");
});

test("report windows are compact and follow the selected tz", () => {
  const begin = Date.UTC(2026, 10, 24, 15, 26) / 1000;
  const end = Date.UTC(2026, 10, 24, 20, 26) / 1000;
  assert.equal(formatUnixWindow(begin, end, "UTC"), "24 Nov 15:26 – 20:26");
  assert.equal(
    formatUnixWindow(begin, end + 8 * 3600, "UTC"),
    "24 Nov 15:26 – 25 Nov 04:26",
  );
  assert.equal(
    formatUnixWindow(begin, end, "Europe/Istanbul"),
    "24 Nov 18:26 – 23:26",
  );
  assert.equal(formatUnixWindow(null, end, "UTC"), "—");
});
