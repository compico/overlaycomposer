// Прилипание виджетов друг к другу и к краям canvas. Чистые функции без DOM — тестируются в Node.
// Все величины — в координатах canvas (пиксели layout), порог тоже: редактор передаёт «экранные пиксели / масштаб».

export interface Rect { x: number; y: number; w: number; h: number }

export interface Guide {
  axis: "x" | "y"; // "x" — вертикальная линия на координате pos, "y" — горизонтальная
  pos: number;
  from: number; // протяжённость линии вдоль другой оси
  to: number;
}

export interface SnapOptions {
  threshold: number;
  canvas?: { width: number; height: number };
}

import { type Transform, visibleBox } from "./types.ts";

const EPS = 0.01;

const xs = (r: Rect) => [r.x, r.x + r.w / 2, r.x + r.w];
const ys = (r: Rect) => [r.y, r.y + r.h / 2, r.y + r.h];

/** Ближайшее допустимое смещение по одной оси: линии движущегося прямоугольника против линий целей. */
function bestDelta(mine: number[], targets: number[], threshold: number): number | null {
  let best: number | null = null;
  for (const m of mine) {
    for (const t of targets) {
      const d = t - m;
      if (Math.abs(d) <= threshold && (best === null || Math.abs(d) < Math.abs(best) - EPS)) best = d;
    }
  }
  return best;
}

function targetsOf(others: Rect[], axis: "x" | "y", canvas?: { width: number; height: number }) {
  const list = others.map((o) => ({ line: axis === "x" ? xs(o) : ys(o), r: o }));
  const out: { v: number; r: Rect | null }[] = [];
  for (const t of list) for (const v of t.line) out.push({ v, r: t.r });
  if (canvas) {
    const size = axis === "x" ? canvas.width : canvas.height;
    for (const v of [0, size / 2, size]) out.push({ v, r: null });
  }
  return out;
}

/** Направляющие по оси для уже сдвинутого прямоугольника: все цели, совпавшие с его линиями. */
function guidesFor(moved: Rect, mineLines: number[], axis: "x" | "y", others: Rect[], canvas?: { width: number; height: number }): Guide[] {
  const out: Guide[] = [];
  for (const t of targetsOf(others, axis, canvas)) {
    if (!mineLines.some((m) => Math.abs(m - t.v) < EPS)) continue;
    const a0 = axis === "x" ? moved.y : moved.x, a1 = a0 + (axis === "x" ? moved.h : moved.w);
    let from = a0, to = a1;
    if (t.r) {
      const b0 = axis === "x" ? t.r.y : t.r.x, b1 = b0 + (axis === "x" ? t.r.h : t.r.w);
      from = Math.min(from, b0);
      to = Math.max(to, b1);
    } else if (canvas) {
      from = 0;
      to = axis === "x" ? canvas.height : canvas.width;
    }
    const dup = out.find((g) => Math.abs(g.pos - t.v) < EPS);
    if (dup) { dup.from = Math.min(dup.from, from); dup.to = Math.max(dup.to, to); } else out.push({ axis, pos: t.v, from, to });
  }
  return out;
}

export interface MoveSnap { dx: number; dy: number; guides: Guide[] }

/**
 * Перемещение: левый/центральный/правый край (и верхний/средний/нижний) прилипают к тем же линиям других виджетов и canvas.
 * Левый край к правому краю соседа даёт «встык» — так собираются ряды и колонки; верх к верху/центр к центру — выравнивание.
 */
export function snapMove(r: Rect, others: Rect[], o: SnapOptions): MoveSnap {
  const dx = bestDelta(xs(r), targetsOf(others, "x", o.canvas).map((t) => t.v), o.threshold);
  const dy = bestDelta(ys(r), targetsOf(others, "y", o.canvas).map((t) => t.v), o.threshold);
  const moved: Rect = { ...r, x: r.x + (dx ?? 0), y: r.y + (dy ?? 0) };
  const guides: Guide[] = [];
  if (dx !== null) guides.push(...guidesFor(moved, xs(moved), "x", others, o.canvas));
  if (dy !== null) guides.push(...guidesFor(moved, ys(moved), "y", others, o.canvas));
  return { dx: dx ?? 0, dy: dy ?? 0, guides };
}

export interface Edges { l?: boolean; r?: boolean; t?: boolean; b?: boolean }

/** Изменение размера без поворота: прилипают только двигаемые края; противоположные остаются на месте. */
export function snapResize(r: Rect, edges: Edges, others: Rect[], o: SnapOptions): { rect: Rect; guides: Guide[] } {
  const out = { ...r };
  const guides: Guide[] = [];
  const tx = targetsOf(others, "x", o.canvas).map((t) => t.v);
  const ty = targetsOf(others, "y", o.canvas).map((t) => t.v);
  if (edges.l || edges.r) {
    const edge = edges.l ? r.x : r.x + r.w;
    const d = bestDelta([edge], tx, o.threshold);
    if (d !== null) {
      if (edges.l) { out.x += d; out.w -= d; } else out.w += d;
    }
  }
  if (edges.t || edges.b) {
    const edge = edges.t ? r.y : r.y + r.h;
    const d = bestDelta([edge], ty, o.threshold);
    if (d !== null) {
      if (edges.t) { out.y += d; out.h -= d; } else out.h += d;
    }
  }
  if (edges.l || edges.r) guides.push(...guidesFor(out, [edges.l ? out.x : out.x + out.w], "x", others, o.canvas));
  if (edges.t || edges.b) guides.push(...guidesFor(out, [edges.t ? out.y : out.y + out.h], "y", others, o.canvas));
  return { rect: out, guides };
}

/** Ограничивающий прямоугольник видимой части виджета на canvas (с учётом масштаба и поворота). */
export function boundsOf(t: Transform): Rect {
  const v = visibleBox(t);
  const hw = (v.w * Math.abs(t.scaleX)) / 2, hh = (v.h * Math.abs(t.scaleY)) / 2;
  const a = (t.rotationZ * Math.PI) / 180;
  const c = Math.abs(Math.cos(a)), s = Math.abs(Math.sin(a));
  const ew = c * hw + s * hh, eh = s * hw + c * hh;
  return { x: v.cx - ew, y: v.cy - eh, w: ew * 2, h: eh * 2 };
}
