// node --experimental-strip-types --test web/shared/snap.test.ts
import assert from "node:assert/strict";
import { test } from "node:test";
import { boundsOf, snapMove, snapResize, type Rect } from "./snap.ts";
import { defaultTransform } from "./types.ts";

const canvas = { width: 1920, height: 1080 };
const opts = { threshold: 6, canvas };
const A: Rect = { x: 100, y: 100, w: 200, h: 100 };

/** Куда придёт прямоугольник w×h, брошенный в (x, y). */
function moveTo(x: number, y: number, w: number, h: number, others: Rect[] = [A]) {
  const m = snapMove({ x, y, w, h }, others, opts);
  return { x: x + m.dx, y: y + m.dy, guides: m.guides };
}

test("встык справа от соседа", () => {
  const r = moveTo(305, 300, 200, 100);
  assert.deepEqual([r.x, r.y], [300, 300]);
});

test("ряд: встык справа и верх вровень", () => {
  const r = moveTo(305, 103, 200, 100);
  assert.deepEqual([r.x, r.y], [300, 100]);
});

test("колонка: встык снизу и левый край вровень", () => {
  const r = moveTo(103, 205, 200, 100);
  assert.deepEqual([r.x, r.y], [100, 200]);
});

test("встык слева от соседа", () => {
  const r = moveTo(-97, 100, 200, 100);
  assert.deepEqual([r.x, r.y], [-100, 100]);
});

test("далеко от соседа — без прилипания", () => {
  const r = moveTo(330, 330, 200, 100);
  assert.deepEqual([r.x, r.y], [330, 330]);
});

test("центр прилипает к центру соседа", () => {
  const r = moveTo(145, 500, 100, 50);
  assert.deepEqual([r.x, r.y], [150, 500]);
});

test("края холста", () => {
  assert.deepEqual(Object.values(moveTo(4, 1000, 200, 100)).slice(0, 2), [0, 1000]);
  assert.deepEqual(Object.values(moveTo(1716, 976, 200, 100)).slice(0, 2), [1720, 980]);
});

test("из нескольких линий выбирается ближайшая", () => {
  const B: Rect = { x: 310, y: 500, w: 50, h: 50 };
  const r = moveTo(104, 700, 200, 100, [A, B]);
  assert.equal(r.x, 100);
});

test("направляющая на линии прилипания", () => {
  const r = moveTo(305, 300, 200, 100);
  assert.ok(r.guides.some((g) => g.axis === "x" && g.pos === 300));
});

test("ресайз: правый край к левому краю соседа", () => {
  const C: Rect = { x: 500, y: 100, w: 100, h: 100 };
  const { rect } = snapResize({ x: 100, y: 100, w: 395, h: 100 }, { r: true }, [C], opts);
  assert.deepEqual([rect.x, rect.w], [100, 400]);
});

test("ресайз: левый край к правому краю соседа, правый край остаётся", () => {
  const { rect } = snapResize({ x: 305, y: 100, w: 95, h: 100 }, { l: true }, [A], opts);
  assert.deepEqual([rect.x, rect.w], [300, 100]);
  assert.equal(rect.x + rect.w, 400);
});

test("ресайз не трогает края, которые не тянут", () => {
  const { rect } = snapResize({ x: 305, y: 103, w: 95, h: 100 }, { l: true }, [A], opts);
  assert.equal(rect.y, 103);
});

test("повёрнутый виджет прилипает габаритной рамкой", () => {
  const t = { ...defaultTransform(), width: 200, height: 100, rotationZ: 90, x: 355 - 100, y: 500 - 50 };
  const b = boundsOf(t);
  assert.deepEqual([Math.round(b.w), Math.round(b.h)], [100, 200]);
  const m = snapMove(b, [A], opts);
  assert.equal(Math.round(b.x + m.dx), 300);
});
