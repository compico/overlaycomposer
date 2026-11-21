import { test } from "node:test";
import assert from "node:assert/strict";
import { isDeleteTile, type DeckKey } from "./keys.ts";

const k = (o: Partial<DeckKey> = {}): DeckKey => ({ key: "Delete", mod: false, alt: false, tag: "BODY", editable: false, dialogOpen: false, ...o });

test("isDeleteTile: Delete и Backspace при выбранной плитке", () => {
  assert.equal(isDeleteTile(k(), "t1"), true);
  assert.equal(isDeleteTile(k({ key: "Backspace" }), "t1"), true);
});

test("isDeleteTile: без выбранной плитки, в полях, в диалоге, с модификаторами и на других клавишах — нет", () => {
  assert.equal(isDeleteTile(k(), null), false);
  for (const tag of ["INPUT", "TEXTAREA", "SELECT"]) assert.equal(isDeleteTile(k({ tag }), "t1"), false);
  assert.equal(isDeleteTile(k({ editable: true }), "t1"), false);
  assert.equal(isDeleteTile(k({ dialogOpen: true }), "t1"), false);
  assert.equal(isDeleteTile(k({ mod: true }), "t1"), false);
  assert.equal(isDeleteTile(k({ alt: true }), "t1"), false);
  assert.equal(isDeleteTile(k({ key: "a" }), "t1"), false);
});
