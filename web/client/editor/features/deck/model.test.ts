import { test } from "node:test";
import assert from "node:assert/strict";
import { fakeApi } from "../../lib/fakeapi.test-helper.ts";
import { ApiError } from "../../lib/api.ts";
import { createDeckModel } from "./model.ts";

const grid = { rows: 4, maxGroupCols: 16, sizes: [{ w: 1, h: 1 }, { w: 2, h: 1 }, { w: 3, h: 1 }, { w: 2, h: 3 }] };
const resp = () => ({
  layout: { rev: 3, groups: [{ id: "g1", name: "Scenes", tiles: [] }] }, saved: false, scenes: ["Chat", "Game"],
  palette: { blue: "#00f", red: "#f00" }, icons: { home: "⌂" }, grid,
});

test("load → правки → save отправляет раскладку с rev и сбрасывает dirty", async () => {
  const { api, calls, bodies } = fakeApi({
    "GET /api/deck/layout": resp,
    "PUT /api/deck/layout": () => ({ rev: 4 }),
  });
  const m = createDeckModel(api);
  await m.load();
  assert.equal(m.get().dirty, false);
  const id = m.addTile("g1", { w: 2, h: 1 })!;
  assert.equal(m.get().selected, id);
  assert.equal(m.get().dirty, true);
  const t = m.get().layout!.groups[0].tiles[0];
  assert.equal(t.action.scene, "Chat", "берётся первая неиспользованная сцена");
  assert.equal(m.addTile("g1", { w: 2, h: 1 }) && m.get().layout!.groups[0].tiles[1].action.scene, "Game");
  await m.save();
  assert.equal(calls.at(-1), "PUT /api/deck/layout");
  assert.equal((bodies.at(-1) as { rev: number }).rev, 3);
  assert.equal(m.get().dirty, false);
  assert.equal(m.get().layout!.rev, 4);
  assert.equal(m.get().saved, true);
});

test("конфликт сохранения пробрасывается, dirty остаётся", async () => {
  const { api } = fakeApi({
    "GET /api/deck/layout": resp,
    "PUT /api/deck/layout": () => { throw new ApiError("conflict", 409, {}); },
  });
  const m = createDeckModel(api);
  await m.load();
  m.addGroup();
  await assert.rejects(m.save(), (e: unknown) => e instanceof ApiError && e.status === 409);
  assert.equal(m.get().dirty, true);
});

test("reset вызывает DELETE и перечитывает раскладку", async () => {
  const { api, calls } = fakeApi({ "GET /api/deck/layout": resp, "DELETE /api/deck/layout": () => ({}) });
  const m = createDeckModel(api);
  await m.load();
  m.addGroup();
  await m.reset();
  assert.deepEqual(calls, ["GET /api/deck/layout", "DELETE /api/deck/layout", "GET /api/deck/layout"]);
  assert.equal(m.get().dirty, false);
  assert.equal(m.get().layout!.groups.length, 1);
});

test("невозможные правки не трогают состояние; удаление выбранной плитки снимает выбор", async () => {
  const { api } = fakeApi({ "GET /api/deck/layout": resp });
  const m = createDeckModel(api);
  await m.load();
  const id = m.addTile("g1", { w: 1, h: 1 })!;
  assert.equal(m.moveTile(id, "g1", 99, 0), false);
  assert.equal(m.updateTile(id, { w: 7, h: 7 }), false);
  m.removeTile(id);
  assert.equal(m.get().selected, null);
  const g = m.addGroup()!;
  const t2 = m.addTile(g, { w: 1, h: 1 })!;
  m.removeGroup(g);
  assert.equal(m.get().selected === t2, false);
});
