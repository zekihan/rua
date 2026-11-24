import { test } from "node:test";
import assert from "node:assert/strict";
import { formatNumber, formatPercent, formatBytes, formatUnixDate, formatUnixDateTime } from "../src/format.ts";
import { stackedSegments, donutArcs, sparklinePoints, barHeight, maxValue } from "../src/chart-helpers.ts";

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

test("chart helpers", () => {
  const { total, segments } = stackedSegments([{ key: "pass", value: 3 }, { key: "fail", value: 1 }]);
  assert.equal(total, 4); assert.equal(segments[0].fraction, 0.75); assert.equal(segments[1].offset, 0.75);
  assert.equal(stackedSegments([{ key: "a", value: 0 }]).total, 0);
  const arcs = donutArcs([{ key: "pass", value: 1 }, { key: "fail", value: 1 }]);
  assert.equal(arcs[0].endAngle, Math.PI);
  assert.equal(maxValue([1, 9, 3]), 9);
  assert.equal(barHeight(5, 10, 100), 50);
  assert.equal(barHeight(5, 0, 100), 0);
  assert.equal(sparklinePoints([], 100, 30), "");
  assert.equal(sparklinePoints([0, 10], 102, 32, 1).split(" ").length, 2);
});
