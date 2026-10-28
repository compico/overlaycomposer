// node --experimental-strip-types --test web/client/editor/features/editor/model.test.ts
import assert from "node:assert/strict";
import { test } from "node:test";
import type { Layout } from "../../../../shared/types.ts";
import { defaultTransform } from "../../../../shared/types.ts";
import { fakeApi } from "../../lib/fakeapi.test-helper.ts";
import { createEditorModel, reorderObjects, type ChangeKind } from "./model.ts";

const obj = (id: string, z: number, x = 0) => ({ id, name: id, sourceId: "s1", transform: { ...defaultTransform(), x }, opacity: 1, visible: true, locked: false, zIndex: z });
const mkLayout = (id = "L1"): Layout => ({ id, name: "Main", scenes: [], canvas: { width: 1920, height: 1080 }, objects: [obj("a", 1), obj("b", 2)] });

function setup(opts: { confirm?: boolean } = {}) {
  const server: Record<string, Layout> = { L1: mkLayout("L1"), L2: { ...mkLayout("L2"), name: "Other", objects: [] } };
  let saved: Layout | null = null;
  const { api, calls } = fakeApi({
    "GET /api/sources": () => [{ id: "s1", name: "Src", type: "iframe", data: {} }],
    "GET /api/plugins": () => [],
    "GET /api/layouts": () => Object.values(server),
    "GET /api/default-layout": () => ({ layoutId: "L1" }),
    "GET /api/state": () => ({ activeLayoutId: "L1", scene: null, obs: { connected: false, scenes: [] } }),
    "GET /api/layouts/L1": () => structuredClone(server.L1),
    "GET /api/layouts/L2": () => structuredClone(server.L2),
    "PUT /api/layouts/L1": (b) => { saved = b as Layout; server.L1 = structuredClone(b as Layout); return {}; },
    "POST /api/layouts": (b) => { server.L3 = { ...mkLayout("L3"), name: (b as { name: string }).name }; return server.L3; },
    "GET /api/layouts/L3": () => structuredClone(server.L3),
    "DELETE /api/layouts/L1": () => { delete server.L1; return {}; },
  });
  const m = createEditorModel({ api, confirmDiscard: () => opts.confirm ?? true, recordDelayMs: 5 });
  const kinds: ChangeKind[] = [];
  m.subscribe((s) => kinds.push(s.change));
  return { m, calls, kinds, server, saved: () => saved };
}
const wait = (ms = 20) => new Promise((r) => setTimeout(r, ms));

test("bootstrap загружает всё и открывает live layout", async () => {
  const { m } = setup();
  await m.bootstrap();
  const s = m.get();
  assert.equal(s.layout?.id, "L1");
  assert.equal(s.sources.length, 1);
  assert.equal(s.activeLayoutId, "L1");
  assert.equal(s.defaultLayoutId, "L1");
  assert.equal(s.dirty, false);
});

test("правка → dirty; отмена возвращает и снимает dirty; повтор возвращает правку", async () => {
  const { m } = setup();
  await m.bootstrap();
  m.get().layout!.objects[0].transform.x = 500;
  m.touch();
  assert.equal(m.get().dirty, true);
  m.flush();
  assert.equal(m.get().canUndo, true);
  assert.equal(m.undo(), true);
  assert.equal(m.get().layout!.objects[0].transform.x, 0);
  assert.equal(m.get().dirty, false);
  assert.equal(m.redo(), true);
  assert.equal(m.get().layout!.objects[0].transform.x, 500);
  assert.equal(m.get().dirty, true);
});

test("быстрые правки склеиваются в один шаг", async () => {
  const { m } = setup();
  await m.bootstrap();
  for (let i = 1; i <= 5; i++) { m.get().layout!.objects[0].transform.x = i; m.touch(); }
  await wait();
  m.undo();
  assert.equal(m.get().layout!.objects[0].transform.x, 0);
  assert.equal(m.get().canUndo, false);
});

test("add/remove/reorder/visible/locked/rename меняют слои и отменяются", async () => {
  const { m } = setup();
  await m.bootstrap();
  const o = m.addObject("s1", 100, 50)!;
  assert.equal(m.get().selectedId, o.id);
  assert.equal(o.zIndex, 3);
  assert.equal(o.name, "Src");
  assert.deepEqual([o.transform.x, o.transform.y], [910, 515]);
  m.setVisible("a", false);
  m.setLocked("b", true);
  m.rename("b", "Renamed");
  m.flush();
  assert.deepEqual(m.get().layout!.objects.map((x) => [x.id, x.visible, x.locked, x.name]), [["a", false, false, "a"], ["b", true, true, "Renamed"], [o.id, true, false, "Src"]]);
  m.removeObject(o.id);
  assert.equal(m.get().selectedId, null);
  m.reorder("a", "b");
  m.flush();
  assert.equal(m.get().layout!.objects.find((x) => x.id === "a")!.zIndex, 2);
  while (m.undo()) { /* до начала */ }
  assert.equal(m.get().layout!.objects.length, 2);
  assert.equal(m.get().dirty, false);
});

test("reorderObjects: верх = больший zIndex; чужие id игнорируются", () => {
  const l = mkLayout();
  assert.equal(reorderObjects(l, "a", "b"), true); // a над b
  assert.deepEqual(l.objects.map((o) => [o.id, o.zIndex]), [["a", 2], ["b", 1]]);
  assert.equal(reorderObjects(l, "a", "zzz"), false);
  assert.equal(reorderObjects(l, "a", "a"), false);
});

test("openLayout: несохранённое — спрашивает; отказ оставляет layout", async () => {
  const { m } = setup({ confirm: false });
  await m.bootstrap();
  m.touch();
  assert.equal(await m.openLayout("L2"), false);
  assert.equal(m.get().layout!.id, "L1");
  assert.equal(await m.openLayout("L2", true), true);
  assert.equal(m.get().layout!.id, "L2");
  assert.equal(m.get().canUndo, false); // история — своя у каждого открытого layout
});

test("save: PUT рабочей копии, dirty снимается, отмена после сохранения работает", async () => {
  const { m, saved } = setup();
  await m.bootstrap();
  m.get().layout!.objects[1].transform.x = 777;
  m.touch();
  await m.save();
  assert.equal(saved()!.objects[1].transform.x, 777);
  assert.equal(m.get().dirty, false);
  assert.equal(m.undo(), true);
  assert.equal(m.get().layout!.objects[1].transform.x, 0);
  assert.equal(m.get().dirty, true);
});

test("reloadCurrent: с несохранёнными правками ничего не трогает; без — подтягивает и делает отдельный шаг", async () => {
  const { m, server } = setup();
  await m.bootstrap();
  server.L1.objects[0].transform.x = 42;
  m.touch();
  await m.reloadCurrent();
  assert.equal(m.get().layout!.objects[0].transform.x, 0);
  m.undo(); // dirty снят
  m.get().layout!.objects[0].transform.x = 0;
  await m.reloadCurrent();
  assert.equal(m.get().layout!.objects[0].transform.x, 42);
  assert.equal(m.get().dirty, false);
  assert.equal(m.undo(), true);
  assert.equal(m.get().layout!.objects[0].transform.x, 0);
});

test("dropSource убирает слои источника без пометки «не сохранено»", async () => {
  const { m } = setup();
  await m.bootstrap();
  m.select("a");
  m.dropSource("s1");
  assert.equal(m.get().layout!.objects.length, 0);
  assert.equal(m.get().selectedId, null);
  assert.equal(m.get().dirty, false);
});

test("create/duplicate/delete layout и сцены OBS", async () => {
  const { m, calls } = setup();
  await m.bootstrap();
  await m.createLayout("New");
  assert.equal(m.get().layout!.id, "L3");
  await m.openLayout("L1", true);
  await m.deleteCurrent();
  assert.ok(calls.includes("DELETE /api/layouts/L1"));
  assert.notEqual(m.get().layout!.id, "L1");
  assert.equal(m.setObsScenes(["A"]), true);
  assert.equal(m.setObsScenes(["A"]), false);
});

test("каждое изменение публикуется с типом (для выборочной перерисовки)", async () => {
  const { m, kinds } = setup();
  await m.bootstrap();
  kinds.length = 0;
  m.select("a");
  m.setVisible("a", false);
  m.setLocked("a", true);
  m.touch();
  assert.deepEqual(kinds.slice(0, 4), ["selection", "structure", "layers", "geometry"]);
});
