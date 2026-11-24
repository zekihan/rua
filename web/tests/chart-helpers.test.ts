import { test } from "node:test";
import assert from "node:assert/strict";
import {
  stackedSegments,
  donutArcs,
  sparklinePoints,
  barHeight,
  maxValue,
  countScale,
  tickIndices,
} from "../src/chart-helpers.ts";

test("chart helpers", () => {
  const { total, segments } = stackedSegments([
    { key: "pass", value: 3 },
    { key: "fail", value: 1 },
  ]);
  assert.equal(total, 4);
  assert.equal(segments[0].fraction, 0.75);
  assert.equal(segments[1].offset, 0.75);
  assert.equal(stackedSegments([{ key: "a", value: 0 }]).total, 0);
  const arcs = donutArcs([
    { key: "pass", value: 1 },
    { key: "fail", value: 1 },
  ]);
  assert.equal(arcs[0].endAngle, Math.PI);
  assert.equal(maxValue([1, 9, 3]), 9);
  assert.equal(barHeight(5, 10, 100), 50);
  assert.equal(barHeight(5, 0, 100), 0);
  assert.equal(sparklinePoints([], 100, 30), "");
  assert.equal(sparklinePoints([0, 10], 102, 32, 1).split(" ").length, 2);
});

test("count scales use integer gridlines and cover their input", () => {
  assert.deepEqual(countScale(0), [0, 1]);
  assert.deepEqual(countScale(3), [0, 1, 2, 3]);
  assert.deepEqual(countScale(760), [0, 200, 400, 600, 800]);
  assert.deepEqual(countScale(10_001), [0, 5_000, 10_000, 15_000]);
});

test("date ticks include the endpoints without exceeding the label budget", () => {
  assert.deepEqual(tickIndices(0, 5), []);
  assert.deepEqual(tickIndices(1, 5), [0]);
  assert.deepEqual(tickIndices(4, 5), [0, 1, 2, 3]);
  assert.deepEqual(tickIndices(365, 5), [0, 91, 182, 273, 364]);
});
