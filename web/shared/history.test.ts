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

test("jumpTo: прыжок назад и вперёд по списку, «будущее» сохраняется до новой правки", () => {
  const x = h("a", "b", "c", "d");
  assert.equal(x.jumpTo(1), "b");
  assert.equal(x.index, 1);
  assert.equal(x.canRedo, true);
  assert.equal(x.jumpTo(3), "d"); // вперёд — тоже можно
  assert.equal(x.jumpTo(0), "a");
  assert.equal(x.undo(), null);
  x.jumpTo(2);
  x.record("e"); // новая правка стирает «будущее»
  assert.deepEqual(x.entries.map((e) => e.label), ["Opened", "Edit", "Edit", "Edit"]);
  assert.equal(x.canRedo, false);
});

test("jumpTo, sad path: за границы, нецелое, на текущий шаг — null и положение не меняется", () => {
  const x = h("a", "b", "c");
  assert.equal(x.jumpTo(-1), null);
  assert.equal(x.jumpTo(3), null);
  assert.equal(x.jumpTo(1.5), null);
  assert.equal(x.jumpTo(2), null); // уже здесь
  assert.equal(x.index, 2);
});

test("entries: подписи, текущий, будущее и сохранённое состояние", () => {
  const x = new History("a");
  x.record("b", "Move Chat");
  x.record("c", "Hide Alerts");
  x.markSaved();
  x.undo();
  assert.deepEqual(x.entries, [
    { index: 0, label: "Opened", current: false, future: false, saved: false },
    { index: 1, label: "Move Chat", current: true, future: false, saved: false },
    { index: 2, label: "Hide Alerts", current: false, future: true, saved: true },
  ]);
  assert.equal(x.dirty, true);
  x.jumpTo(2);
  assert.equal(x.dirty, false);
});

test("version растёт при записи, отмене, повторе и прыжке, но не при пустой записи", () => {
  const x = h("a");
  const v0 = x.version;
  assert.equal(x.record("a"), false);
  assert.equal(x.version, v0);
  x.record("b");
  x.undo();
  x.redo();
  x.jumpTo(0);
  assert.equal(x.version, v0 + 4);
});

test("лимит: самые старые шаги отбрасываются вместе с подписями", () => {
  const x = new History("0", 3);
  x.record("1", "one");
  x.record("2", "two");
  x.record("3", "three");
  assert.deepEqual(x.entries.map((e) => e.label), ["one", "two", "three"]);
  assert.equal(x.index, 2);
});
