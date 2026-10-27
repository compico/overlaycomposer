// node --experimental-strip-types --test web/shared/history.test.ts
import assert from "node:assert/strict";
import { test } from "node:test";
import { History } from "./history.ts";

const h = (...states: string[]) => {
  const x = new History(states[0]);
  for (const s of states.slice(1)) x.record(s);
  return x;
};

test("отмена возвращает предыдущее состояние, повтор — отменённое", () => {
  const x = h("100", "400");
  assert.equal(x.undo(), "100");
  assert.equal(x.redo(), "400");
});

test("несколько правок отменяются по одной в обратном порядке", () => {
  const x = h("100", "200", "300");
  assert.equal(x.undo(), "200");
  assert.equal(x.undo(), "100");
  assert.equal(x.undo(), null);
});

test("новая правка после отмены стирает «будущее»", () => {
  const x = h("100", "200");
  x.undo();
  x.record("300");
  assert.equal(x.canRedo, false);
  assert.equal(x.undo(), "100");
});

test("правка без изменений не засоряет историю", () => {
  const x = h("100");
  assert.equal(x.record("100"), false);
  assert.equal(x.canUndo, false);
});

test("отмена и повтор там, где некуда, ничего не ломают", () => {
  const x = h("100");
  assert.equal(x.undo(), null);
  assert.equal(x.redo(), null);
  assert.equal(x.current, "100");
});

test("dirty: возврат к сохранённому состоянию снимает пометку; после Save отмена снова её ставит", () => {
  const x = h("100", "400");
  assert.equal(x.dirty, true);
  x.undo();
  assert.equal(x.dirty, false);
  x.redo();
  x.markSaved();
  assert.equal(x.dirty, false);
  x.undo();
  assert.equal(x.current, "100");
  assert.equal(x.dirty, true);
});

test("лимит: самые старые шаги забываются", () => {
  const x = new History("0", 3);
  for (const s of ["1", "2", "3", "4"]) x.record(s);
  assert.equal(x.undoDepth, 2);
  assert.equal(x.undo(), "3");
  assert.equal(x.undo(), "2");
  assert.equal(x.undo(), null);
});
