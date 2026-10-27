import assert from "node:assert/strict";
import { boundsOf, snapMove, snapResize, type Rect } from "../../shared/snap.ts";
import { defaultTransform } from "../../shared/types.ts";
import type { Steps, World } from "../run.ts";

const opts = (w: World) => ({ threshold: w.threshold, canvas: w.canvas });

export function register(s: Steps) {
  s.step("холст {int}x{int} и порог прилипания {int} пикселей", (w, cw, ch, th) => {
    w.canvas = { width: cw, height: ch };
    w.threshold = th;
    w.others = [] as Rect[];
  });
  s.step('на холсте стоит виджет {string} в x={int} y={int} размером {int}x{int}', (w, _name, x, y, ww, hh) => {
    w.others.push({ x, y, w: ww, h: hh });
  });
  s.step("я двигаю виджет {int}x{int} в x={int} y={int}", (w, ww, hh, x, y) => {
    const r: Rect = { x, y, w: ww, h: hh };
    const m = snapMove(r, w.others, opts(w));
    w.result = { x: r.x + m.dx, y: r.y + m.dy };
    w.guides = m.guides;
  });
  s.step("я двигаю виджет {int}x{int}, повёрнутый на {int}°, так что его центр в x={int} y={int}", (w, ww, hh, deg, cx, cy) => {
    const t = { ...defaultTransform(), width: ww, height: hh, rotationZ: deg, x: cx - ww / 2, y: cy - hh / 2 };
    const b = boundsOf(t);
    const m = snapMove(b, w.others, opts(w));
    w.bounds = { x: b.x + m.dx, w: b.w };
  });
  s.step("виджет оказывается в x={int} y={int}", (w, x, y) => {
    assert.deepEqual([Math.round(w.result.x), Math.round(w.result.y)], [x, y]);
  });
  s.step("габаритная рамка виджета оказывается в x={int} при ширине {int}", (w, x, ww) => {
    assert.deepEqual([Math.round(w.bounds.x), Math.round(w.bounds.w)], [x, ww]);
  });
  s.step("есть вертикальная направляющая на x={int}", (w, x) => {
    assert.ok(w.guides.some((g: any) => g.axis === "x" && Math.abs(g.pos - x) < 0.01), `направляющие: ${JSON.stringify(w.guides)}`);
  });
  s.step("я тяну правый край виджета x={int} w={int} до соседа", (w, x, ww) => {
    w.rect = snapResize({ x, y: 100, w: ww, h: 100 }, { r: true }, w.others, opts(w)).rect;
  });
  s.step("я тяну левый край виджета x={int} w={int} до соседа", (w, x, ww) => {
    w.rect = snapResize({ x, y: 100, w: ww, h: 100 }, { l: true }, w.others, opts(w)).rect;
  });
  s.step("ширина виджета становится {int} при левом крае x={int}", (w, ww, x) => {
    assert.deepEqual([Math.round(w.rect.w), Math.round(w.rect.x)], [ww, x]);
  });
}
