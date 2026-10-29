// node --experimental-strip-types --test web/client/editor/features/panels.models.test.ts
import assert from "node:assert/strict";
import { test } from "node:test";
import { layerName, layerRows } from "./layers/model.ts";
import { layoutRows } from "./layouts/model.ts";
import type { LayoutObject } from "../../../shared/types.ts";

const obj = (id: string, z: number, extra: Partial<LayoutObject> = {}) =>
  ({ id, name: "", sourceId: "s1", zIndex: z, visible: true, locked: false, ...extra }) as LayoutObject;

test("слои: верхний первым, имя из источника, выбор и флаги", () => {
  const rows = layerRows([obj("a", 1), obj("b", 3, { name: "Chat", visible: false }), obj("c", 2, { locked: true })], [{ id: "s1", name: "Now playing" }], "c");
  assert.deepEqual(rows.map((r) => r.id), ["b", "c", "a"]);
  assert.equal(rows[0].name, "Chat");
  assert.equal(rows[1].name, "Now playing");
  assert.deepEqual(rows.map((r) => r.selected), [false, true, false]);
  assert.equal(rows[0].visible, false);
  assert.equal(rows[1].locked, true);
});

test("слои, sad path: источник удалён → пометка missing; пустой список", () => {
  assert.equal(layerName({ name: "", sourceId: "gone" }, []), "(missing gone)");
  assert.deepEqual(layerRows([], [], null), []);
});

test("layouts: default / live / число сцен / подсказка", () => {
  const rows = layoutRows(
    [{ id: "1", name: "Main", scenes: ["A", "B"] }, { id: "2", name: "Empty", scenes: [] }],
    "1", "2", "1",
  );
  assert.equal(rows[0].selected, true);
  assert.equal(rows[0].live, true);
  assert.equal(rows[0].sceneCount, 2);
  assert.equal(rows[0].title, "OBS scenes: A, B");
  assert.equal(rows[1].isDefault, true);
  assert.equal(rows[1].title, "No OBS scenes bound");
});
