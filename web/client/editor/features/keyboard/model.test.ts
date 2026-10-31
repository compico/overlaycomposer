// node --experimental-strip-types --test web/client/editor/features/keyboard/model.test.ts
import assert from "node:assert/strict";
import { test } from "node:test";
import { type KeyContext, type KeyInput, resolveKey, shortcutKey } from "./model.ts";

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

// Русская раскладка: на клавише Z — «я», на S — «ы», на Y — «н», на C — «с», на V — «м», на L — «д».
test("сочетания работают в русской раскладке (по физической клавише)", () => {
  const ru = (k: string, code: string, extra: Partial<KeyInput> = {}) => key(k, { code, ...extra });
  assert.deepEqual(cmd(ru("я", "KeyZ", { mod: true })), { type: "undo" });
  assert.deepEqual(cmd(ru("Я", "KeyZ", { mod: true, shift: true })), { type: "redo" });
  assert.deepEqual(cmd(ru("н", "KeyY", { mod: true })), { type: "redo" });
  assert.deepEqual(cmd(ru("ы", "KeyS", { mod: true })), { type: "save" });
  assert.deepEqual(cmd(ru("ы", "KeyS", { mod: true, tag: "INPUT", inputType: "text" })), { type: "save" });
  assert.deepEqual(cmd(ru("с", "KeyC", { mod: true })), { type: "copy" });
  assert.deepEqual(cmd(ru("м", "KeyV", { mod: true }), ctx({ hasClip: true })), { type: "paste" });
  assert.deepEqual(cmd(ru("д", "KeyL")), { type: "toggleCompose" });
  // в текстовом поле Ctrl+Я по-прежнему оставляет родную отмену браузера
  assert.equal(cmd(ru("я", "KeyZ", { mod: true, tag: "INPUT", inputType: "text" })), undefined);
});

test("shortcutKey: латиница — по букве (Dvorak/QWERTZ), знаки — не буквы, без code остаётся как есть", () => {
  assert.equal(shortcutKey("z", "KeyY"), "z"); // QWERTZ: физическая Y даёт «z» — Ctrl+Z там по букве
  assert.equal(shortcutKey("Z", "KeyZ"), "z");
  assert.equal(shortcutKey(";", "KeyZ"), ";"); // Dvorak: «;» на месте Z — не отмена
  assert.equal(shortcutKey("я", undefined), "я"); // нет code (старый браузер) — прежнее поведение
  assert.equal(shortcutKey("я", "Digit1"), "я"); // code не буква
  assert.equal(shortcutKey("Escape", "Escape"), "escape");
  assert.equal(cmd(key(";", { code: "KeyZ", mod: true })), undefined);
});
