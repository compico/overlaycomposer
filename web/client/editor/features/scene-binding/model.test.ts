// node --experimental-strip-types --test web/client/editor/features/scene-binding/model.test.ts
import assert from "node:assert/strict";
import { test } from "node:test";
import { addScene, sceneRows, setBound } from "./model.ts";

const cur = { id: "a", name: "Main", scenes: ["Gaming", "Old"] };
const other = { id: "b", name: "Chat", scenes: ["Chatting", "BRB"] };

test("строки: сцены OBS + привязанные, которых в OBS нет; пометки", () => {
  const rows = sceneRows(cur, [cur, other], ["Gaming", "Chatting", "BRB"]);
  assert.deepEqual(rows.map((r) => r.name), ["Gaming", "Chatting", "BRB", "Old"]);
  assert.deepEqual(rows.map((r) => r.checked), [true, false, false, true]);
  assert.deepEqual(rows[1].notes, ["used by “Chat”"]);
  assert.deepEqual(rows[3].notes, ["not in OBS"]);
});

test("привязка сцены, которую держит другой layout: пометка «moves from»", () => {
  const c = { ...cur, scenes: [...cur.scenes, "Chatting"] };
  const row = sceneRows(c, [c, other], ["Chatting"]).find((r) => r.name === "Chatting")!;
  assert.deepEqual(row.notes, ["moves from “Chat”"]);
});

test("без списка сцен OBS пометка «not in OBS» не показывается", () => {
  assert.deepEqual(sceneRows(cur, [cur], []).map((r) => r.notes), [[], []]);
});

test("setBound и addScene не меняют исходный массив и не дублируют", () => {
  const src = ["A"];
  assert.deepEqual(setBound(src, "B", true), ["A", "B"]);
  assert.deepEqual(setBound(src, "A", false), []);
  assert.equal(setBound(src, "A", true), src);
  assert.deepEqual(src, ["A"]);
  assert.equal(addScene(src, "  "), src);
  assert.equal(addScene(src, "A"), src);
  assert.deepEqual(addScene(src, " C "), ["A", "C"]);
});
