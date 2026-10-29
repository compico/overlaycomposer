// node --experimental-strip-types --test web/client/editor/features/properties/model.test.ts
import assert from "node:assert/strict";
import { test } from "node:test";
import type { LayoutObject, Source, Transform } from "../../../../shared/types.ts";
import { fakeApi } from "../../lib/fakeapi.test-helper.ts";
import { canvasFromInputs, createPropertiesModel, MIN_SIZE, resetCrop, ROUTER_MODES, setCrop, transformFields } from "./model.ts";

const tr = (extra: Partial<Transform> = {}): Transform =>
  ({ x: 100, y: 100, z: 0, width: 400, height: 300, rotationX: 0, rotationY: 0, rotationZ: 0, scaleX: 1, scaleY: 1, scaleZ: 1, ...extra }) as Transform;
const obj = (t = tr()) => ({ id: "o", opacity: 1, transform: t }) as unknown as LayoutObject;

test("setCrop: не отрицательная, оставляет видимую часть, содержимое не двигается", () => {
  const t = tr();
  const before = { x: t.x, y: t.y };
  setCrop(t, "cropLeft", 100);
  assert.equal(t.cropLeft, 100);
  // содержимое осталось на месте: окно сдвинулось на обрезанную часть — x тот же, т.к. offset хранится в visibleBox
  assert.equal(t.y, before.y);
  setCrop(t, "cropLeft", -5);
  assert.equal(t.cropLeft, 0);
  setCrop(t, "cropLeft", 10_000);
  assert.equal(t.cropLeft, 400 - MIN_SIZE);
  setCrop(t, "cropRight", 10_000);
  assert.equal(t.cropRight, 0); // слева уже съедено всё, кроме MIN_SIZE
});

test("resetCrop обнуляет все стороны", () => {
  const t = tr();
  setCrop(t, "cropLeft", 50);
  setCrop(t, "cropTop", 30);
  resetCrop(t);
  assert.deepEqual([t.cropLeft, t.cropTop, t.cropRight, t.cropBottom], [0, 0, 0, 0]);
});

test("transformFields: границы ширины/высоты и прозрачности", () => {
  const o = obj(tr({ cropLeft: 50, cropRight: 50 }));
  const f = Object.fromEntries(transformFields(o).map((x) => [x.label, x]));
  f["Width"].set(10);
  assert.equal(o.transform.width, 101); // не меньше суммы обрезок + 1
  f["Height"].set(-5);
  assert.equal(o.transform.height, 1);
  f["Opacity %"].set(250);
  assert.equal(o.opacity, 1);
  f["Opacity %"].set(-3);
  assert.equal(o.opacity, 0);
  f["Opacity %"].set(50);
  assert.equal(f["Opacity %"].get(), 50);
  f["X"].set(7);
  assert.equal(o.transform.x, 7);
});

test("canvasFromInputs: пустое → умолчание, минимум 16", () => {
  assert.deepEqual(canvasFromInputs("", ""), { width: 1920, height: 1080 });
  assert.deepEqual(canvasFromInputs("5", "abc"), { width: 16, height: 1080 });
  assert.deepEqual(canvasFromInputs("1280", "720"), { width: 1280, height: 720 });
});

test("операции над источником и default layout", async () => {
  const f = fakeApi({
    "PUT /api/sources/s1": () => ({}), "DELETE /api/sources/s1": () => ({}), "PUT /api/default-layout": () => ({}),
  });
  const m = createPropertiesModel(f.api);
  const src = { id: "s1" } as Source;
  await m.renameSource(src, "N");
  await m.setSourceUrl(src, "http://x");
  await m.setSourceProxy(src, true);
  await m.deleteSource(src);
  await m.setDefaultLayout(null);
  assert.deepEqual(f.bodies, [{ name: "N" }, { data: { url: "http://x" } }, { data: { proxy: true } }, undefined, { layoutId: null }]);
});

test("sad path: ошибка сервера пробрасывается вызывающему", async () => {
  const f = fakeApi({ "PUT /api/sources/s1": () => { throw new Error("relative url"); } });
  await assert.rejects(createPropertiesModel(f.api).setSourceProxy({ id: "s1" } as Source, true), /relative url/);
});

test("режим SPA-роутера: PUT с data.router и обновление источника; ошибка не меняет источник", async () => {
  const f = fakeApi({ "PUT /api/sources/s1": () => ({}) });
  const m = createPropertiesModel(f.api);
  const src = { id: "s1", data: { url: "https://x", proxy: true } } as Source;
  await m.setSourceRouter(src, "on");
  assert.deepEqual(f.bodies[0], { data: { router: "on" } });
  assert.equal(src.data.router, "on");
  await m.setSourceRouter(src, "");
  assert.equal(src.data.router, "");
  assert.deepEqual(ROUTER_MODES.map((x) => x.value), ["", "on", "off"]);

  const bad = fakeApi({ "PUT /api/sources/s1": () => { throw new Error("invalid router"); } });
  const s2 = { id: "s1", data: { router: "off" } } as Source;
  await assert.rejects(createPropertiesModel(bad.api).setSourceRouter(s2, "on"), /invalid router/);
  assert.equal(s2.data.router, "off");
});
