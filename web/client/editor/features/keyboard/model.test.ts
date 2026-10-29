// node --experimental-strip-types --test web/client/editor/features/keyboard/model.test.ts
import assert from "node:assert/strict";
import { test } from "node:test";
import { type KeyContext, type KeyInput, resolveKey } from "./model.ts";

const key = (k: string, extra: Partial<KeyInput> = {}): KeyInput => ({ key: k, mod: false, alt: false, shift: false, tag: "BODY", ...extra });
const ctx = (extra: Partial<KeyContext> = {}): KeyContext => ({ hasSelection: true, selectionLocked: false, hasClip: false, ...extra });
const cmd = (k: KeyInput, c: KeyContext = ctx()) => resolveKey(k, c)?.command;

test("Ctrl+S, Ctrl+Z, Ctrl+Shift+Z, Ctrl+Y", () => {
  assert.deepEqual(cmd(key("s", { mod: true })), { type: "save" });
  assert.deepEqual(cmd(key("z", { mod: true })), { type: "undo" });
  assert.deepEqual(cmd(key("Z", { mod: true, shift: true })), { type: "redo" });
  assert.deepEqual(cmd(key("y", { mod: true })), { type: "redo" });
});

test("в поле ввода Ctrl+Z — родная отмена, а Ctrl+S работает", () => {
  assert.equal(cmd(key("z", { mod: true, tag: "INPUT", inputType: "text" })), undefined);
  assert.equal(cmd(key("z", { mod: true, tag: "TEXTAREA" })), undefined);
  assert.deepEqual(cmd(key("s", { mod: true, tag: "INPUT", inputType: "text" })), { type: "save" });
  // чекбокс — не текстовое поле
  assert.deepEqual(cmd(key("z", { mod: true, tag: "INPUT", inputType: "checkbox" })), { type: "undo" });
});

test("copy/paste требуют выбор и буфер", () => {
  assert.deepEqual(cmd(key("c", { mod: true })), { type: "copy" });
  assert.equal(cmd(key("c", { mod: true }), ctx({ hasSelection: false })), undefined);
  assert.equal(cmd(key("v", { mod: true })), undefined);
  assert.deepEqual(cmd(key("v", { mod: true }), ctx({ hasClip: true, hasSelection: false })), { type: "paste" });
});

test("L переключает режим компоновки, но не с модификаторами и не в поле", () => {
  assert.deepEqual(cmd(key("l"), ctx({ hasSelection: false })), { type: "toggleCompose" });
  assert.equal(cmd(key("l", { mod: true }), ctx({ hasSelection: false })), undefined);
  assert.equal(cmd(key("l", { tag: "INPUT", inputType: "text" })), undefined);
});

test("стрелки двигают на 1 или 10 (Shift); заблокированный слой не двигается; без выбора — ничего", () => {
  assert.deepEqual(cmd(key("ArrowLeft")), { type: "nudge", dx: -1, dy: 0 });
  assert.deepEqual(cmd(key("ArrowDown", { shift: true })), { type: "nudge", dx: 0, dy: 10 });
  assert.equal(cmd(key("ArrowUp"), ctx({ selectionLocked: true })), undefined);
  assert.equal(cmd(key("ArrowUp"), ctx({ hasSelection: false })), undefined);
});

test("Delete/Backspace удаляют, Escape снимает выбор без preventDefault", () => {
  assert.deepEqual(resolveKey(key("Delete"), ctx()), { command: { type: "delete" }, prevent: true });
  assert.deepEqual(resolveKey(key("Escape"), ctx()), { command: { type: "deselect" }, prevent: false });
  assert.equal(resolveKey(key("Delete", { tag: "INPUT", inputType: "text" }), ctx()), null);
});
