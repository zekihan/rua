import { test } from "node:test";
import assert from "node:assert/strict";
import { collapseRows } from "../src/receivers.ts";

test("receivers collapse past the limit and expand on request", () => {
  const rows = [1, 2, 3, 4, 5, 6, 7];
  assert.deepEqual(collapseRows(rows, false, 5), {
    shown: [1, 2, 3, 4, 5],
    hidden: 2,
  });
  assert.deepEqual(collapseRows(rows, true, 5), { shown: rows, hidden: 0 });
  assert.deepEqual(collapseRows([1, 2], false, 5), {
    shown: [1, 2],
    hidden: 0,
  });
  assert.deepEqual(collapseRows([1, 2, 3, 4, 5], false, 5).hidden, 0);
});
