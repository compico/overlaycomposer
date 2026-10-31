// Математика жестов на канвасе: перемещение, ресайз, обрезка, поворот, попадание курсора, масштаб вида.
// Чистые функции без DOM — тестируются в Node. Координаты — пиксели canvas layout; «экран» = canvas * k.
import type { Guide, Rect } from "./snap.ts";
import { contentCenter, type LayoutObject, placeByContentCenter, type Transform, visibleBox } from "./types.ts";

/** Минимальный размер видимой части виджета, px. */
export const MIN_SIZE = 20;

// ---------- обрезка ----------

export type CropSide = "cropLeft" | "cropTop" | "cropRight" | "cropBottom";

/** Задать одну сторону обрезки с ограничениями (≥ 0, остаётся видимая часть) и сохранением положения содержимого. */
export function setCrop(t: Transform, side: CropSide, value: number, keep = contentCenter(t)) {
  const other = side === "cropLeft" ? t.cropRight : side === "cropRight" ? t.cropLeft : side === "cropTop" ? t.cropBottom : t.cropTop;
  const total = side === "cropLeft" || side === "cropRight" ? t.width : t.height;
  const max = Math.max(0, total - (other ?? 0) - MIN_SIZE);
  t[side] = Math.round(Math.min(max, Math.max(0, value)));
  placeByContentCenter(t, keep);
}

/** Сбросить обрезку так, чтобы содержимое осталось на месте. */
export function resetCrop(t: Transform) {
  const f = contentCenter(t);
  t.cropLeft = t.cropRight = t.cropTop = t.cropBottom = 0;
  placeByContentCenter(t, f);
}

/**
 * Alt+перетаскивание ручки (dirx/diry ∈ {-1,0,1}) — обрезка как в OBS. Угол обрезает сразу две стороны;
 * содержимое остаётся на месте, сдвигается только граница видимой части. Поворот и масштаб учитываются.
 * Возвращает функцию, применяющую смещение указателя (dx, dy в px canvas от начала жеста) к `t`.
 */
export function beginCrop(t: Transform, dirx: number, diry: number): (dx: number, dy: number) => void {
  const c0 = visibleBox(t);
  const f = contentCenter(t);
  const th = (t.rotationZ * Math.PI) / 180;
  const cos = Math.cos(th), sin = Math.sin(th);
  const sx = Math.abs(t.scaleX) || 1, sy = Math.abs(t.scaleY) || 1;
  return (dx, dy) => {
    // дельта указателя в локальных пикселях виджета (до масштаба)
    const ldx = (dx * cos + dy * sin) / sx;
    const ldy = (-dx * sin + dy * cos) / sy;
    if (dirx < 0) setCrop(t, "cropLeft", c0.cl + ldx, f);
    if (dirx > 0) setCrop(t, "cropRight", c0.cr - ldx, f);
    if (diry < 0) setCrop(t, "cropTop", c0.ct + ldy, f);
    if (diry > 0) setCrop(t, "cropBottom", c0.cb - ldy, f);
  };
}

// ---------- ресайз ----------

export interface ResizeOptions {
  /** Shift на угловой ручке: сохранять пропорции. */
  keepAspect?: boolean;
  /**
   * Прилипание краёв. Получает желаемый прямоугольник (до поворота) и какие края двигаются; возвращает поправленный.
   * Не вызывается при повороте и при keepAspect — пропорции важнее.
   */
  snap?(r: Rect, edges: { l: boolean; r: boolean; t: boolean; b: boolean }): { rect: Rect; guides: Guide[] };
}

/**
 * Ресайз за ручку (dirx/diry ∈ {-1,0,1}). Противоположная сторона остаётся на месте (с учётом поворота и масштаба),
 * обрезка сохраняется: меняется размер видимой части, полный размер виджета растёт вместе с ней.
 * Возвращает функцию: смещение указателя от начала жеста → правит `t`, отдаёт направляющие для рисования.
 */
export function beginResize(t: Transform, dirx: number, diry: number): (dx: number, dy: number, opts?: ResizeOptions) => Guide[] {
  const v0 = visibleBox(t);
  const sxAbs = Math.abs(t.scaleX) || 1;
  const syAbs = Math.abs(t.scaleY) || 1;
  const w0 = v0.w * sxAbs, h0 = v0.h * syAbs; // видимая часть на canvas
  const cx0 = v0.cx, cy0 = v0.cy;
  const rotation = t.rotationZ;
  const th = (rotation * Math.PI) / 180;
  const cos = Math.cos(th), sin = Math.sin(th);

  return (dx, dy, opts = {}) => {
    // дельта указателя в локальной (повёрнутой) системе объекта
    const ldx = dx * cos + dy * sin;
    const ldy = -dx * sin + dy * cos;
    let w = dirx ? Math.max(MIN_SIZE, w0 + dirx * ldx) : w0;
    let h = diry ? Math.max(MIN_SIZE, h0 + diry * ldy) : h0;
    const aspect = !!opts.keepAspect && !!dirx && !!diry;
    if (aspect) {
      const r = w0 / h0;
      if (w / w0 > h / h0) h = w / r; else w = h * r;
    }
    let guides: Guide[] = [];
    if (opts.snap && !opts.keepAspect && !rotation) {
      const left = cx0 - w0 / 2, top = cy0 - h0 / 2;
      const r: Rect = { x: dirx < 0 ? left + w0 - w : left, y: diry < 0 ? top + h0 - h : top, w, h };
      const sn = opts.snap(r, { l: dirx < 0, r: dirx > 0, t: diry < 0, b: diry > 0 });
      w = Math.max(MIN_SIZE, sn.rect.w);
      h = Math.max(MIN_SIZE, sn.rect.h);
      guides = sn.guides;
    }
    // противоположная сторона остаётся на месте: центр смещается на половину прироста
    const lx = (dirx * (w - w0)) / 2;
    const ly = (diry * (h - h0)) / 2;
    const cx = cx0 + lx * cos - ly * sin;
    const cy = cy0 + lx * sin + ly * cos;
    t.width = Math.round(w / sxAbs) + v0.cl + v0.cr;
    t.height = Math.round(h / syAbs) + v0.ct + v0.cb;
    t.x = Math.round(cx - v0.ox - t.width / 2);
    t.y = Math.round(cy - v0.oy - t.height / 2);
    return guides;
  };
}

// ---------- поворот ----------

/** Угол (−180…180, шаг 0.1°) для указателя (px, py) при центре вращения (cx, cy). Ручка сверху = 0°. */
export function rotationAngle(cx: number, cy: number, px: number, py: number, snap15 = false): number {
  let a = (Math.atan2(py - cy, px - cx) * 180) / Math.PI + 90;
  if (a > 180) a -= 360;
  if (snap15) a = Math.round(a / 15) * 15;
  return Math.round(a * 10) / 10;
}

// ---------- попадание и вид ----------

/** Видимые объекты под точкой (px, py) в координатах canvas, сверху вниз; учитывает обрезку, масштаб и поворот. */
export function objectsAtPoint(objects: LayoutObject[], px: number, py: number): LayoutObject[] {
  return [...objects]
    .filter((o) => o.visible)
    .sort((a, b) => b.zIndex - a.zIndex)
    .filter((o) => {
      const t = o.transform;
      const v = visibleBox(t);
      const hw = (v.w * Math.abs(t.scaleX)) / 2, hh = (v.h * Math.abs(t.scaleY)) / 2;
      const a = (-t.rotationZ * Math.PI) / 180;
      const dx = px - v.cx, dy = py - v.cy;
      const lx = dx * Math.cos(a) - dy * Math.sin(a), ly = dx * Math.sin(a) + dy * Math.cos(a);
      return Math.abs(lx) <= hw && Math.abs(ly) <= hh;
    });
}

/** Масштаб «экранных px на 1 px canvas», чтобы canvas W×H поместился во вьюпорт с полями по 20px. */
export function fitScale(viewportW: number, viewportH: number, w: number, h: number): number {
  return Math.max(0.05, Math.min((viewportW - 40) / w, (viewportH - 40) / h));
}

/** Рамка видимой части объекта на экране (в px экрана, до поворота). */
export function boxRect(t: Transform, k: number): { left: number; top: number; width: number; height: number; rotation: number } {
  const v = visibleBox(t);
  const width = v.w * Math.abs(t.scaleX) * k;
  const height = v.h * Math.abs(t.scaleY) * k;
  return { left: v.cx * k - width / 2, top: v.cy * k - height / 2, width, height, rotation: t.rotationZ };
}
