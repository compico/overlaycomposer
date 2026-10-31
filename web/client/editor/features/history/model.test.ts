// node --experimental-strip-types --test web/client/editor/features/history/model.test.ts
import assert from "node:assert/strict";
import { test } from "node:test";
import { History } from "../../../../shared/history.ts";
import { historyRows, historySummary } from "./model.ts";

function sample() {
  const h = new History("0");
  h.record("1", "Add Chat");
  h.record("2", "Move Chat");
  h.record("3", "Hide Chat");
  h.undo();
  return h.entries;
}

test("строки: новые сверху, текущий и будущее отмечены, подсказки про шаги", () => {
  const rows = historyRows(sample());
  assert.deepEqual(rows.map((r) => r.label), ["Hide Chat", "Move Chat", "Add Chat", "Opened"]);
  assert.deepEqual(rows.map((r) => r.current), [false, true, false, false]);
  assert.deepEqual(rows.map((r) => r.future), [true, false, false, false]);
  assert.deepEqual(rows.map((r) => r.canJump), [true, false, true, true]);
  assert.equal(rows[0].hint, "Go forward 1 step");
  assert.equal(rows[1].hint, "Current state");
  assert.equal(rows[2].hint, "Go back 1 step");
  assert.equal(rows[3].hint, "Go back 2 steps");
});

test("сводка для кнопки", () => {
  assert.equal(historySummary(sample()), "History — 2 to undo, 1 to redo");
  assert.equal(historySummary(new History("0").entries), "History — 0 to undo");
});

test("sad path: пустая история", () => {
  assert.deepEqual(historyRows([]), []);
  assert.equal(historySummary([]), "No history");
});
