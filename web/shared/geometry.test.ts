// Запуск: node --experimental-strip-types web/shared/geometry.test.ts (Node 22.6+) — геометрия обрезки без браузера.
import assert from "node:assert/strict";
import { contentCenter, defaultTransform, placeByContentCenter, visibleBox } from "./types.ts";

const near = (a: number, b: number, m = "") => assert.ok(Math.abs(a - b) < 1.5, `${m}: ${a} ≠ ${b}`);

// без обрезки видимая часть = весь виджет
{
  const t = { ...defaultTransform(), x: 10, y: 20, width: 400, height: 300 };
  const v = visibleBox(t);
  assert.deepEqual([v.w, v.h, v.cx, v.cy], [400, 300, 210, 170]);
}

// обрезка слева на 100: видимая ширина 300, центр сдвинулся на +50
{
  const t = { ...defaultTransform(), x: 0, y: 0, width: 400, height: 300, cropLeft: 100 };
  const v = visibleBox(t);
  assert.deepEqual([v.w, v.h, v.ox, v.cx], [300, 300, 50, 250]);
}

// содержимое остаётся на месте при любой обрезке, повороте и масштабе
for (const [rot, sx, sy] of [[0, 1, 1], [30, 1, 1], [-75, 1.5, 0.8], [180, 2, 2]]) {
  const t = { ...defaultTransform(), x: 300, y: 200, width: 500, height: 400, rotationZ: rot, scaleX: sx, scaleY: sy };
  const f0 = contentCenter(t);
  t.cropLeft = 120; t.cropBottom = 60; t.cropTop = 30;
  placeByContentCenter(t, f0);
  const f1 = contentCenter(t);
  near(f1.x, f0.x, `x rot=${rot}`);
  near(f1.y, f0.y, `y rot=${rot}`);
}
console.log("geometry ok");
