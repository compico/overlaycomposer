// node --experimental-strip-types --test web/shared/gestures.test.ts
import assert from "node:assert/strict";
import { test } from "node:test";
import { beginCrop, beginResize, boxRect, fitScale, MIN_SIZE, objectsAtPoint, resetCrop, rotationAngle, setCrop } from "./gestures.ts";
import { type LayoutObject, type Transform, visibleBox } from "./types.ts";

const tr = (extra: Partial<Transform> = {}): Transform =>
  ({ x: 100, y: 100, z: 0, width: 400, height: 200, rotationX: 0, rotationY: 0, rotationZ: 0, scaleX: 1, scaleY: 1, scaleZ: 1, ...extra }) as Transform;
const obj = (id: string, z: number, t: Transform, visible = true) => ({ id, zIndex: z, visible, transform: t }) as unknown as LayoutObject;

test("ресайз за правый край: левый остаётся на месте", () => {
  const t = tr();
  beginResize(t, 1, 0)(50, 0);
  assert.deepEqual([t.x, t.y, t.width, t.height], [100, 100, 450, 200]);
});

test("ресайз за левый-верхний угол: правый-нижний остаётся на месте", () => {
  const t = tr();
  beginResize(t, -1, -1)(-30, -20);
  assert.deepEqual([t.x, t.y, t.width, t.height], [70, 80, 430, 220]);
});

test("ресайз не уменьшает видимую часть меньше MIN_SIZE", () => {
  const t = tr();
  beginResize(t, 1, 1)(-10_000, -10_000);
  assert.deepEqual([t.width, t.height], [MIN_SIZE, MIN_SIZE]);
});

test("ресайз с Shift на углу сохраняет пропорции", () => {
  const t = tr(); // 400×200 → 2:1
  beginResize(t, 1, 1)(100, 5, { keepAspect: true });
  assert.equal(t.width / t.height, 2);
  assert.equal(t.width, 500);
});

test("ресайз при повороте на 90°: противоположный край неподвижен", () => {
  const t = tr({ rotationZ: 90 });
  const before = visibleBox(t);
  // при повороте на 90° «правый» край объекта смотрит вниз: тянем указатель вниз на 40px
  beginResize(t, 1, 0)(0, 40);
  assert.equal(t.width, 440);
  const after = visibleBox(t);
  // центр уехал на половину прироста вдоль локальной оси X (она направлена вниз)
  assert.ok(Math.abs(after.cx - before.cx) < 1);
  assert.ok(Math.abs(after.cy - (before.cy + 20)) < 1);
});

test("ресайз с обрезкой: размер виджета растёт вместе с видимой частью, обрезка сохраняется", () => {
  const t = tr({ cropLeft: 50, cropRight: 30 });
  beginResize(t, 1, 0)(10, 0);
  assert.equal(t.cropLeft, 50);
  assert.equal(t.cropRight, 30);
  assert.equal(t.width, 410);
});

test("ресайз: прилипание вызывается без поворота и Shift, направляющие возвращаются", () => {
  const t = tr();
  let seen: unknown = null;
  const guides = beginResize(t, 1, 0)(7, 0, {
    snap(r, edges) { seen = { r, edges }; return { rect: { ...r, w: 500 }, guides: [{ axis: "x", pos: 600, from: 0, to: 10 }] }; },
  });
  assert.deepEqual((seen as { edges: object }).edges, { l: false, r: true, t: false, b: false });
  assert.equal(t.width, 500);
  assert.equal(guides.length, 1);
  // с поворотом и с Shift — не вызывается
  let called = false;
  beginResize(tr({ rotationZ: 10 }), 1, 0)(7, 0, { snap: (r) => { called = true; return { rect: r, guides: [] }; } });
  beginResize(tr(), 1, 1)(7, 7, { keepAspect: true, snap: (r) => { called = true; return { rect: r, guides: [] }; } });
  assert.equal(called, false);
});

test("setCrop: ≥ 0, остаётся MIN_SIZE, содержимое на месте", () => {
  const t = tr();
  const c0 = { x: t.x + t.width / 2, y: t.y + t.height / 2 };
  setCrop(t, "cropLeft", 100);
  assert.equal(t.cropLeft, 100);
  const v = visibleBox(t);
  assert.equal(v.cx - v.ox, c0.x); // центр всего виджета не сдвинулся
  setCrop(t, "cropLeft", -5);
  assert.equal(t.cropLeft, 0);
  setCrop(t, "cropLeft", 10_000);
  assert.equal(t.cropLeft, 400 - MIN_SIZE);
  resetCrop(t);
  assert.deepEqual([t.cropLeft, t.cropRight, t.cropTop, t.cropBottom], [0, 0, 0, 0]);
});

test("beginCrop: правая и нижняя ручки режут справа/снизу; угол — две стороны; поворот учитывается", () => {
  const t = tr();
  beginCrop(t, 1, 1)(-40, -10);
  assert.deepEqual([t.cropRight, t.cropBottom], [40, 10]);
  const r = tr({ rotationZ: 90 });
  beginCrop(r, 1, 0)(0, -25); // локальная ось X смотрит вниз; смещение вверх = внутрь
  assert.equal(r.cropRight, 25);
});

test("rotationAngle: ручка сверху = 0°, справа = 90°, шаг 15° с Shift", () => {
  assert.equal(rotationAngle(0, 0, 0, -100), 0);
  assert.equal(rotationAngle(0, 0, 100, 0), 90);
  assert.equal(rotationAngle(0, 0, 0, 100), 180);
  assert.equal(rotationAngle(0, 0, -100, 0), -90);
  assert.equal(rotationAngle(0, 0, 100, -80, true) % 15, 0);
});

test("objectsAtPoint: сверху вниз, скрытые пропускаются, учтены поворот и обрезка", () => {
  const a = obj("a", 1, tr());
  const b = obj("b", 2, tr({ x: 150 }));
  const hidden = obj("h", 3, tr(), false);
  assert.deepEqual(objectsAtPoint([a, b, hidden], 300, 200).map((o) => o.id), ["b", "a"]);
  assert.deepEqual(objectsAtPoint([a, b], 110, 110).map((o) => o.id), ["a"]);
  assert.deepEqual(objectsAtPoint([a], 1000, 1000), []);
  // повёрнутый на 90° прямоугольник 400×200 вокруг центра (300,200) становится вертикальным
  const rot = obj("r", 1, tr({ rotationZ: 90 }));
  assert.equal(objectsAtPoint([rot], 300, 350).length, 1);
  assert.equal(objectsAtPoint([rot], 480, 200).length, 0);
  // обрезанная часть не ловит курсор
  const crop = obj("c", 1, tr({ cropLeft: 200 }));
  assert.equal(objectsAtPoint([crop], 150, 200).length, 0);
  assert.equal(objectsAtPoint([crop], 400, 200).length, 1);
});

test("fitScale и boxRect", () => {
  assert.equal(fitScale(1040, 600, 1920, 1080), Math.min(1000 / 1920, 560 / 1080));
  assert.equal(fitScale(10, 10, 1920, 1080), 0.05);
  const r = boxRect(tr({ rotationZ: 30 }), 0.5);
  assert.deepEqual([r.left, r.top, r.width, r.height, r.rotation], [50, 50, 200, 100, 30]);
});
