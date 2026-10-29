// Канвас редактора: рамки и ручки поверх предпросмотра, перетаскивание, ресайз, обрезка, поворот, направляющие.
// DOM и pointer-события здесь; математика — в shared/gestures.ts и shared/snap.ts; состояние — у модели editor.
import { beginCrop, beginResize, boxRect, fitScale, objectsAtPoint, rotationAngle } from "../../../../shared/gestures.ts";
import { boundsOf, type Guide, type Rect, snapMove, snapResize } from "../../../../shared/snap.ts";
import type { Layout, LayoutObject } from "../../../../shared/types.ts";
import { visibleBox } from "../../../../shared/types.ts";
import { el } from "../../lib/dom.ts";

export interface CanvasRefs {
  frame: HTMLElement;
  viewport: HTMLElement;
  overlay: HTMLElement;
  guides: HTMLElement;
}

export interface CanvasPorts {
  state(): { layout: Layout | null; selectedId: string | null };
  /** Подпись слоя (в режиме компоновки и во всплывающей подсказке). */
  name(o: LayoutObject): string;
  compose(): boolean;
  /** Прилипание включено (без Ctrl на время жеста). */
  snap(e: PointerEvent): boolean;
  select(id: string | null): void;
  /** Слой правится жестом: пометить несохранённым, обновить предпросмотр и поля свойств. */
  edited(o: LayoutObject): void;
  /** Жест закончен — один шаг истории. */
  gestureEnd(): void;
  /** Показать layout в предпросмотре / подогнать масштаб. */
  setLayout(l: Layout): void;
  fit(scale: number): void;
}

const SNAP_PX = 8; // порог прилипания в экранных пикселях

const CURSORS: Record<string, string> = {
  "-1,-1": "nwse-resize", "1,1": "nwse-resize", "1,-1": "nesw-resize", "-1,1": "nesw-resize",
  "0,-1": "ns-resize", "0,1": "ns-resize", "-1,0": "ew-resize", "1,0": "ew-resize",
};

export function mountCanvas(refs: CanvasRefs, ports: CanvasPorts) {
  let k = 1; // экранных px на 1 px canvas

  /** Рамки других видимых виджетов — цели прилипания. */
  function snapTargets(except: LayoutObject): Rect[] {
    const { layout } = ports.state();
    return layout ? layout.objects.filter((o) => o.visible && o !== except).map((o) => boundsOf(o.transform)) : [];
  }

  function drawGuides(guides: Guide[]) {
    refs.guides.replaceChildren(...guides.map((g) => {
      const d = el("div", { class: "guide" });
      if (g.axis === "x") { d.style.left = `${g.pos * k}px`; d.style.top = `${g.from * k}px`; d.style.width = "1px"; d.style.height = `${(g.to - g.from) * k}px`; }
      else { d.style.top = `${g.pos * k}px`; d.style.left = `${g.from * k}px`; d.style.height = "1px"; d.style.width = `${(g.to - g.from) * k}px`; }
      return d;
    }));
  }

  function positionBox(box: HTMLElement, o: LayoutObject) {
    const r = boxRect(o.transform, k); // рамка — видимая (необрезанная) часть
    box.style.left = `${r.left}px`;
    box.style.top = `${r.top}px`;
    box.style.width = `${r.width}px`;
    box.style.height = `${r.height}px`;
    box.style.transform = `rotate(${r.rotation}deg)`;
  }

  /** Применить изменение объекта во время drag или из поля свойств: сцена, рамка, поля. */
  function touch(o: LayoutObject) {
    ports.edited(o);
    const box = refs.overlay.querySelector<HTMLElement>(`[data-id="${o.id}"]`);
    if (box) positionBox(box, o);
  }

  function track(ev: PointerEvent, onMove: (dx: number, dy: number, e: PointerEvent) => void) {
    ev.preventDefault();
    const sx = ev.clientX, sy = ev.clientY;
    const move = (e: PointerEvent) => onMove((e.clientX - sx) / k, (e.clientY - sy) / k, e);
    const up = () => {
      window.removeEventListener("pointermove", move);
      window.removeEventListener("pointerup", up);
      window.removeEventListener("pointercancel", up);
      drawGuides([]);
      ports.gestureEnd();
    };
    window.addEventListener("pointermove", move);
    window.addEventListener("pointerup", up);
    window.addEventListener("pointercancel", up);
  }

  /** Видимые объекты под курсором (сверху вниз) с учётом масштаба и поворота. */
  function objectsAt(ev: PointerEvent): LayoutObject[] {
    const { layout } = ports.state();
    if (!layout) return [];
    const r = refs.overlay.getBoundingClientRect();
    return objectsAtPoint(layout.objects, (ev.clientX - r.left) / k, (ev.clientY - r.top) / k);
  }

  function startMove(ev: PointerEvent, o: LayoutObject) {
    const t = o.transform;
    const x0 = t.x, y0 = t.y;
    track(ev, (dx, dy, e) => {
      const { layout } = ports.state();
      let nx = Math.round(x0 + dx), ny = Math.round(y0 + dy);
      let guides: Guide[] = [];
      if (ports.snap(e) && layout) {
        const m = snapMove(boundsOf({ ...t, x: nx, y: ny }), snapTargets(o), { threshold: SNAP_PX / k, canvas: layout.canvas });
        nx += Math.round(m.dx);
        ny += Math.round(m.dy);
        guides = m.guides;
      }
      t.x = nx;
      t.y = ny;
      touch(o);
      drawGuides(guides);
    });
  }

  function startResize(ev: PointerEvent, o: LayoutObject, dirx: number, diry: number) {
    const apply = beginResize(o.transform, dirx, diry);
    track(ev, (dx, dy, e) => {
      const { layout } = ports.state();
      drawGuides(apply(dx, dy, {
        keepAspect: e.shiftKey,
        snap: ports.snap(e) && layout
          ? (r, edges) => snapResize(r, edges, snapTargets(o), { threshold: SNAP_PX / k, canvas: layout.canvas })
          : undefined,
      }));
      touch(o);
    });
  }

  /** Alt+перетаскивание ручки — обрезка (как в OBS). */
  function startCrop(ev: PointerEvent, o: LayoutObject, dirx: number, diry: number) {
    const apply = beginCrop(o.transform, dirx, diry);
    track(ev, (dx, dy) => { apply(dx, dy); touch(o); });
  }

  function startRotate(ev: PointerEvent, o: LayoutObject) {
    const t = o.transform;
    const { cx, cy } = visibleBox(t); // вращение вокруг центра видимой части (как в OBS)
    const rect = refs.frame.getBoundingClientRect();
    track(ev, (_dx, _dy, e) => {
      t.rotationZ = rotationAngle(cx, cy, (e.clientX - rect.left) / k, (e.clientY - rect.top) / k, e.shiftKey);
      touch(o);
    });
  }

  function render() {
    const layer = refs.overlay;
    layer.replaceChildren();
    const { layout, selectedId } = ports.state();
    if (!layout) return;

    const objs = [...layout.objects].sort((a, b) => a.zIndex - b.zIndex);
    for (const o of objs) {
      if (!o.visible) continue;
      const box = el("div", { class: `box${o.id === selectedId ? " sel" : ""}${o.locked ? " locked" : ""}` });
      box.dataset.id = o.id;
      box.style.pointerEvents = "auto";
      box.title = ports.name(o);
      positionBox(box, o);
      if (ports.compose()) box.append(el("span", { class: "tag" }, ports.name(o)));

      // Выбранный (и не заблокированный) объект рисуем поверх всех рамок: клик внутри него двигает именно его,
      // а не тот, что лежит выше. Нижний объект выбирается кликом вне выбранного, из панели Layers или Alt+клик.
      if (o.id === selectedId && !o.locked) box.style.zIndex = "1000000";

      box.addEventListener("pointerdown", (ev) => {
        if (ev.button !== 0) return;
        ev.stopPropagation();
        if (ev.altKey) { // Alt+клик: выбрать следующий объект под курсором (по кругу)
          const hits = objectsAt(ev);
          if (hits.length > 0) {
            const i = hits.findIndex((h) => h.id === ports.state().selectedId);
            ports.select(hits[(i + 1) % hits.length].id);
          }
          return;
        }
        if (ports.state().selectedId !== o.id) ports.select(o.id);
        if (o.locked) return;
        startMove(ev, o);
      });

      if (o.id === selectedId && !o.locked) {
        for (const [dirx, diry] of [[-1, -1], [0, -1], [1, -1], [1, 0], [1, 1], [0, 1], [-1, 1], [-1, 0]]) {
          const h = el("div", { class: "handle" });
          h.style.left = `${(dirx + 1) * 50}%`;
          h.style.top = `${(diry + 1) * 50}%`;
          h.style.cursor = CURSORS[`${dirx},${diry}`];
          h.addEventListener("pointerdown", (ev) => {
            if (ev.button !== 0) return;
            ev.stopPropagation();
            if (ev.altKey) startCrop(ev, o, dirx, diry); // Alt+ручка — обрезка, как в OBS
            else startResize(ev, o, dirx, diry);
          });
          box.append(h);
        }
        const line = el("div", { class: "rotline" });
        line.style.left = "50%";
        line.style.top = "-26px";
        line.style.height = "26px";
        const rot = el("div", { class: "handle rot" });
        rot.style.left = "50%";
        rot.style.top = "-26px";
        rot.addEventListener("pointerdown", (ev) => {
          if (ev.button !== 0) return;
          ev.stopPropagation();
          startRotate(ev, o);
        });
        box.append(line, rot);
      }
      layer.append(box);
    }
  }

  function fit() {
    const { layout } = ports.state();
    if (!layout) return;
    const { width: W, height: H } = layout.canvas;
    k = fitScale(refs.viewport.clientWidth, refs.viewport.clientHeight, W, H);
    refs.frame.style.width = `${W * k}px`;
    refs.frame.style.height = `${H * k}px`;
    refs.frame.style.left = `${(refs.viewport.clientWidth - W * k) / 2}px`;
    refs.frame.style.top = `${(refs.viewport.clientHeight - H * k) / 2}px`;
    ports.fit(k);
  }

  /** Показать текущий layout: предпросмотр, масштаб, рамки. */
  function refresh() {
    const { layout } = ports.state();
    if (!layout) {
      refs.frame.style.display = "none";
      return;
    }
    refs.frame.style.display = "";
    ports.setLayout(layout);
    fit();
    render();
  }

  refs.viewport.addEventListener("pointerdown", (e) => {
    if (!(e.target as HTMLElement).closest(".box")) ports.select(null);
  });
  addEventListener("resize", () => { fit(); render(); });

  return { render, refresh, touch };
}

