// node --experimental-strip-types --test web/client/editor/features/copy/model.test.ts
import assert from "node:assert/strict";
import { test } from "node:test";
import type { Layout, LayoutObject } from "../../../../shared/types.ts";
import { fakeApi } from "../../lib/fakeapi.test-helper.ts";
import { cloneObject, createCopyModel, type CopyEditorPort } from "./model.ts";

const obj = (id: string, extra: Partial<LayoutObject> = {}) =>
  ({ id, name: "", sourceId: "s1", zIndex: 1, visible: true, locked: false, opacity: 1, transform: { x: 10, y: 20 }, ...extra }) as unknown as LayoutObject;
const lay = (id: string, objects: LayoutObject[]) => ({ id, name: id.toUpperCase(), objects, scenes: [] }) as unknown as Layout;

test("cloneObject: новый id, сверху по zIndex, связь только если в целевом layout нет такого instanceId", () => {
  const o = obj("a", { instanceId: "i1" });
  const free = cloneObject(o, lay("t", [obj("x", { zIndex: 5 })]), { linked: true });
  assert.notEqual(free.id, "a");
  assert.equal(free.zIndex, 6);
  assert.equal(free.instanceId, "i1");
  const busy = cloneObject(o, lay("t", [obj("y", { instanceId: "i1" })]), { linked: true });
  assert.notEqual(busy.instanceId, "i1");
  const unlinked = cloneObject(o, lay("t", []), { linked: false });
  assert.notEqual(unlinked.instanceId, "i1");
});

test("cloneObject: сдвиг и подпись (copy), оригинал не меняется", () => {
  const o = obj("a");
  const c = cloneObject(o, lay("t", []), { linked: false, offset: 20, copySuffix: true }, [{ id: "s1", name: "Chat" }]);
  assert.equal(c.transform.x, 30);
  assert.equal(c.name, "Chat (copy)");
  assert.equal(o.transform.x, 10);
});

function setup(dirty = false) {
  const a = lay("a", [obj("o1")]);
  const b = lay("b", [obj("o2", { instanceId: "zz" })]);
  const toasts: string[] = [];
  const calls = { save: 0, dirty: 0, inserted: [] as LayoutObject[], loads: 0 };
  const state = { layout: a, layouts: [a, b], sources: [{ id: "s1", name: "Chat" }], selectedId: "o1" as string | null, dirty };
  const editor: CopyEditorPort = {
    get: () => state as ReturnType<CopyEditorPort["get"]>,
    markDirty: () => { calls.dirty++; },
    save: async () => { calls.save++; },
    insertObject: (o) => { calls.inserted.push(o); },
    loadLayouts: async () => { calls.loads++; },
  };
  const f = fakeApi({ "GET /api/layouts/b": () => b, "PUT /api/layouts/b": () => ({}) });
  const model = createCopyModel({ api: f.api, editor, toast: (m) => toasts.push(m) });
  return { model, calls, toasts, f, a, b, state };
}

test("копия в другой layout: связанная → сохраняет исходный layout, пишет цель и обновляет список", async () => {
  const { model, calls, f, b, toasts } = setup();
  await model.copyToLayout("b", true);
  assert.equal(calls.save, 1);
  assert.equal(calls.loads, 1);
  assert.deepEqual(f.calls, ["GET /api/layouts/b", "PUT /api/layouts/b"]);
  assert.equal(b.objects.length, 2);
  assert.ok(b.objects[1].instanceId);
  assert.deepEqual(toasts, ["Copied to “B”"]);
});

test("связанная копия при несохранённых правках: не сохраняем, предупреждаем", async () => {
  const { model, calls, toasts } = setup(true);
  await model.copyToLayout("b", true);
  assert.equal(calls.save, 0);
  assert.match(toasts[0], /Save this layout/);
});

test("буфер: вставка в тот же layout — со сдвигом и суффиксом, hasClip", async () => {
  const { model, calls } = setup();
  assert.equal(model.hasClip(), false);
  model.paste(); // sad path: буфер пуст
  assert.equal(calls.inserted.length, 0);
  await model.copySelected();
  assert.equal(model.hasClip(), true);
  model.paste();
  assert.equal(calls.inserted.length, 1);
  assert.equal(calls.inserted[0].transform.x, 30);
  assert.equal(calls.inserted[0].name, "Chat (copy)");
});

test("sad path: без выбора копировать нечего; targets не включает открытый layout", async () => {
  const { model, state, f } = setup();
  assert.deepEqual(model.targets().map((l) => l.id), ["b"]);
  state.selectedId = null;
  assert.equal(model.selection(), null);
  await model.copySelected();
  await model.copyToLayout("b", false);
  assert.equal(model.hasClip(), false);
  assert.deepEqual(f.calls, []);
});
