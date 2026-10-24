import { PluginHost } from "../shared/pluginhost.ts";
import type { Layout, LayoutObject, ObsInfo, PluginManifest, PluginSetting, ServerMessage, Source, Transform } from "../../shared/types.ts";
import { contentCenter, defaultTransform, placeByContentCenter, uid, visibleBox } from "../../shared/types.ts";
import { OverlayScene } from "../shared/scene.ts";

const $ = <T extends HTMLElement = HTMLElement>(id: string) => document.getElementById(id) as T;

const MIN_SIZE = 20;

// ---------- state ----------

let layouts: Layout[] = [];
let sources: Source[] = [];
let plugins: PluginManifest[] = [];
let layout: Layout | null = null; // рабочая копия, сохраняется кнопкой Save
let selectedId: string | null = null;
let dirty = false;
let k = 1; // экранных px на 1 px canvas
let obsScenes: string[] = [];
/** layout для сцен без собственного layout */
let defaultLayoutId: string | null = null;
let activeLayoutId: string | null = null;
let propSync: (() => void)[] = [];
/** Буфер Ctrl+C: снимок слоя и layout, из которого он скопирован (живёт, пока открыта страница). */
let clip: { obj: LayoutObject; fromLayoutId: string } | null = null;

// Предпросмотр плагинов в редакторе: чат читается, но писать (chat.say) нельзя — ответы шлёт только оверлей.
const pluginHost = new PluginHost({ send: (m) => wsSend(m), canSend: false });
const scene = new OverlayScene($("stage"), { interactive: true, plugins: pluginHost });

// ---------- helpers ----------

async function api<T = unknown>(path: string, method = "GET", body?: unknown): Promise<T> {
  const res = await fetch(path, {
    method,
    headers: body !== undefined
      ? { "content-type": "application/json", "x-overlay-composer": "1" }
      : { "x-overlay-composer": "1" },
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  if (!res.ok) {
    const e = await res.json().catch(() => ({ error: res.statusText }));
    throw new Error(e.error ?? res.statusText);
  }
  return res.json() as Promise<T>;
}

let toastTimer = 0;
function toast(msg: string) {
  const t = $("toast");
  t.textContent = msg;
  // popover попадает в top layer — иначе тост прячется за открытой модалкой (<dialog>.showModal())
  try { if (!t.matches(":popover-open")) t.showPopover(); } catch { /* старый браузер: обычный fixed */ }
  t.classList.add("show");
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => {
    t.classList.remove("show");
    setTimeout(() => { try { if (!t.classList.contains("show")) t.hidePopover(); } catch { /* ignore */ } }, 250);
  }, 2200) as unknown as number;
}

function fail(e: unknown) {
  console.error(e);
  toast(`Error: ${e instanceof Error ? e.message : String(e)}`);
}

function el<K extends keyof HTMLElementTagNameMap>(
  tag: K,
  props: Partial<Omit<HTMLElementTagNameMap[K], "style">> & { class?: string; style?: string } = {},
  ...children: (Node | string)[]
): HTMLElementTagNameMap[K] {
  const e = document.createElement(tag);
  const { class: cls, style, ...rest } = props as Record<string, unknown>;
  if (cls) e.className = cls as string;
  if (style) e.style.cssText = style as string;
  Object.assign(e, rest);
  for (const c of children) e.append(c);
  return e;
}

const sourceOf = (id: string) => sources.find((s) => s.id === id);
const selected = () => layout?.objects.find((o) => o.id === selectedId) ?? null;
const objName = (o: LayoutObject) => o.name || (sourceOf(o.sourceId)?.name ?? `(missing ${o.sourceId})`);

function markDirty() {
  dirty = true;
  updateBadges();
}

function updateBadges() {
  $("dirtyBadge").textContent = dirty ? "● unsaved" : "";
  $("canvasSize").textContent = layout ? `${layout.canvas.width}×${layout.canvas.height}` : "";
}

// ---------- loading ----------

async function loadSources() {
  sources = await api<Source[]>("/api/sources");
  scene.setSources(sources);
}

async function loadPlugins() {
  plugins = await api<PluginManifest[]>("/api/plugins"); // сервер пересматривает папку plugins/ при каждом запросе
}

async function loadLayouts() {
  const [list, def] = await Promise.all([
    api<Layout[]>("/api/layouts"),
    api<{ layoutId: string | null }>("/api/default-layout"),
  ]);
  layouts = list;
  defaultLayoutId = def.layoutId;
  renderSceneBox(); // привязки могли измениться
}

async function openLayout(id: string, force = false) {
  if (!force && dirty && !confirm("Discard unsaved changes?")) {
    renderLayouts(); // вернуть select на текущий layout
    return;
  }
  layout = await api<Layout>(`/api/layouts/${encodeURIComponent(id)}`);
  selectedId = null;
  dirty = false;
  refreshAll();
}

async function init() {
  await Promise.all([loadSources(), loadLayouts(), loadPlugins()]);
  if (layouts.length === 0) {
    const l = await api<Layout>("/api/layouts", "POST", { name: "Layout 1" });
    layouts = [l];
  }
  const st = await api<{ activeLayoutId: string | null; scene: string | null; obs: ObsInfo }>("/api/state");
  activeLayoutId = st.activeLayoutId;
  applyObsInfo(st.obs);
  await openLayout(st.activeLayoutId && layouts.some((l) => l.id === st.activeLayoutId) ? st.activeLayoutId : layouts[0].id, true);
  connectWs();
}

// ---------- rendering ----------

function refreshAll() {
  renderLayouts();
  refreshCanvas();
  renderLayers();
  renderProps();
  updateBadges();
}

function refreshCanvas() {
  const frame = $("canvasFrame");
  if (!layout) {
    frame.style.display = "none";
    return;
  }
  frame.style.display = "";
  scene.setLayout(layout);
  fitView();
  renderOverlay();
}

function fitView() {
  if (!layout) return;
  const vp = $("viewport");
  const { width: W, height: H } = layout.canvas;
  k = Math.max(0.05, Math.min((vp.clientWidth - 40) / W, (vp.clientHeight - 40) / H));
  const frame = $("canvasFrame");
  frame.style.width = `${W * k}px`;
  frame.style.height = `${H * k}px`;
  frame.style.left = `${(vp.clientWidth - W * k) / 2}px`;
  frame.style.top = `${(vp.clientHeight - H * k) / 2}px`;
  scene.fit({ scale: k, offsetX: 0, offsetY: 0 });
}

const CURSORS: Record<string, string> = {
  "-1,-1": "nwse-resize", "1,1": "nwse-resize", "1,-1": "nesw-resize", "-1,1": "nesw-resize",
  "0,-1": "ns-resize", "0,1": "ns-resize", "-1,0": "ew-resize", "1,0": "ew-resize",
};

function renderOverlay() {
  const layer = $("overlayLayer");
  layer.replaceChildren();
  if (!layout) return;

  const objs = [...layout.objects].sort((a, b) => a.zIndex - b.zIndex);
  for (const o of objs) {
    if (!o.visible) continue;
    const box = el("div", { class: `box${o.id === selectedId ? " sel" : ""}${o.locked ? " locked" : ""}` });
    box.dataset.id = o.id;
    box.style.pointerEvents = "auto";
    box.title = objName(o);
    positionBox(box, o);

    // Выбранный (и не заблокированный) объект рисуем поверх всех рамок: клик внутри него двигает именно его,
    // а не тот, что лежит выше. Нижний объект выбирается кликом вне выбранного, из панели Layers или Alt+клик.
    if (o.id === selectedId && !o.locked) box.style.zIndex = "1000000";

    box.addEventListener("pointerdown", (ev) => {
      if (ev.button !== 0) return;
      ev.stopPropagation();
      if (ev.altKey) { // Alt+клик: выбрать следующий объект под курсором (по кругу)
        const hits = objectsAt(ev);
        if (hits.length > 0) {
          const i = hits.findIndex((h) => h.id === selectedId);
          select(hits[(i + 1) % hits.length].id);
        }
        return;
      }
      if (selectedId !== o.id) select(o.id);
      if (o.locked) return;
      const t = o.transform;
      const x0 = t.x, y0 = t.y;
      track(ev, (dx, dy) => {
        t.x = Math.round(x0 + dx);
        t.y = Math.round(y0 + dy);
        touch(o);
      });
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

/** Видимые объекты под курсором (сверху вниз) с учётом масштаба и поворота. */
function objectsAt(ev: PointerEvent): LayoutObject[] {
  if (!layout) return [];
  const r = $("overlayLayer").getBoundingClientRect();
  const px = (ev.clientX - r.left) / k, py = (ev.clientY - r.top) / k;
  return [...layout.objects]
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

function positionBox(box: HTMLElement, o: LayoutObject) {
  const t = o.transform;
  const v = visibleBox(t); // рамка — видимая (необрезанная) часть
  const w = v.w * Math.abs(t.scaleX) * k;
  const h = v.h * Math.abs(t.scaleY) * k;
  box.style.left = `${v.cx * k - w / 2}px`;
  box.style.top = `${v.cy * k - h / 2}px`;
  box.style.width = `${w}px`;
  box.style.height = `${h}px`;
  box.style.transform = `rotate(${t.rotationZ}deg)`;
}

/** Применить изменение объекта во время drag: сцена + рамка + поля свойств. */
function touch(o: LayoutObject) {
  markDirty();
  scene.setLayout(layout!);
  const box = $("overlayLayer").querySelector<HTMLElement>(`[data-id="${o.id}"]`);
  if (box) positionBox(box, o);
  propSync.forEach((f) => f());
}

function track(ev: PointerEvent, onMove: (dx: number, dy: number, e: PointerEvent) => void) {
  ev.preventDefault();
  const sx = ev.clientX, sy = ev.clientY;
  const move = (e: PointerEvent) => onMove((e.clientX - sx) / k, (e.clientY - sy) / k, e);
  const up = () => {
    window.removeEventListener("pointermove", move);
    window.removeEventListener("pointerup", up);
    window.removeEventListener("pointercancel", up);
  };
  window.addEventListener("pointermove", move);
  window.addEventListener("pointerup", up);
  window.addEventListener("pointercancel", up);
}

function startResize(ev: PointerEvent, o: LayoutObject, dirx: number, diry: number) {
  const t = o.transform;
  const v0 = visibleBox(t);
  const sxAbs = Math.abs(t.scaleX) || 1;
  const syAbs = Math.abs(t.scaleY) || 1;
  const w0 = v0.w * sxAbs, h0 = v0.h * syAbs; // видимая часть на canvas
  const cx0 = v0.cx, cy0 = v0.cy;
  const th = (t.rotationZ * Math.PI) / 180;
  const cos = Math.cos(th), sin = Math.sin(th);

  track(ev, (dx, dy, e) => {
    // дельта указателя в локальной (повёрнутой) системе объекта
    const ldx = dx * cos + dy * sin;
    const ldy = -dx * sin + dy * cos;
    let w = dirx ? Math.max(MIN_SIZE, w0 + dirx * ldx) : w0;
    let h = diry ? Math.max(MIN_SIZE, h0 + diry * ldy) : h0;
    if (e.shiftKey && dirx && diry) {
      const r = w0 / h0;
      if (w / w0 > h / h0) h = w / r; else w = h * r;
    }
    // противоположная сторона остаётся на месте: центр смещается на половину прироста
    const lx = (dirx * (w - w0)) / 2;
    const ly = (diry * (h - h0)) / 2;
    const cx = cx0 + lx * cos - ly * sin;
    const cy = cy0 + lx * sin + ly * cos;
    // меняется размер видимой части; обрезка остаётся, полный размер виджета растёт вместе с ней
    t.width = Math.round(w / sxAbs) + v0.cl + v0.cr;
    t.height = Math.round(h / syAbs) + v0.ct + v0.cb;
    t.x = Math.round(cx - v0.ox - t.width / 2);
    t.y = Math.round(cy - v0.oy - t.height / 2);
    touch(o);
  });
}

type CropSide = "cropLeft" | "cropTop" | "cropRight" | "cropBottom";

/** Задать одну сторону обрезки с ограничениями (≥ 0, остаётся видимая часть) и сохранением положения содержимого. */
function setCrop(t: Transform, side: CropSide, value: number, keep = contentCenter(t)) {
  const other = side === "cropLeft" ? t.cropRight : side === "cropRight" ? t.cropLeft : side === "cropTop" ? t.cropBottom : t.cropTop;
  const total = side === "cropLeft" || side === "cropRight" ? t.width : t.height;
  const max = Math.max(0, total - (other ?? 0) - MIN_SIZE);
  t[side] = Math.round(Math.min(max, Math.max(0, value)));
  placeByContentCenter(t, keep);
}

function cropField(label: string, t: Transform, side: CropSide): HTMLLabelElement {
  const input = el("input", { type: "number" });
  input.min = "0";
  const sync = () => {
    if (document.activeElement !== input) input.value = String(t[side] ?? 0);
  };
  sync();
  propSync.push(sync);
  input.addEventListener("input", () => {
    const v = parseFloat(input.value);
    if (Number.isNaN(v)) return;
    setCrop(t, side, v);
    markDirty();
    scene.setLayout(layout!);
    renderOverlay();
    propSync.forEach((f) => f());
  });
  return el("label", {}, label, input);
}

/**
 * Alt+перетаскивание ручки — обрезка (как в OBS). Угол обрезает сразу две стороны. Содержимое виджета остаётся на месте,
 * сдвигается только граница видимой части; поворот и масштаб учитываются.
 */
function startCrop(ev: PointerEvent, o: LayoutObject, dirx: number, diry: number) {
  const t = o.transform;
  const c0 = visibleBox(t);
  const f = contentCenter(t);
  const th = (t.rotationZ * Math.PI) / 180;
  const cos = Math.cos(th), sin = Math.sin(th);
  const sx = Math.abs(t.scaleX) || 1, sy = Math.abs(t.scaleY) || 1;

  track(ev, (dx, dy) => {
    // дельта указателя в локальных пикселях виджета (до масштаба)
    const ldx = (dx * cos + dy * sin) / sx;
    const ldy = (-dx * sin + dy * cos) / sy;
    if (dirx < 0) setCrop(t, "cropLeft", c0.cl + ldx, f);
    if (dirx > 0) setCrop(t, "cropRight", c0.cr - ldx, f);
    if (diry < 0) setCrop(t, "cropTop", c0.ct + ldy, f);
    if (diry > 0) setCrop(t, "cropBottom", c0.cb - ldy, f);
    touch(o);
  });
}

function startRotate(ev: PointerEvent, o: LayoutObject) {
  const t = o.transform;
  const { cx, cy } = visibleBox(t); // вращение вокруг центра видимой части (как в OBS)
  const rect = $("canvasFrame").getBoundingClientRect();
  track(ev, (_dx, _dy, e) => {
    const px = (e.clientX - rect.left) / k;
    const py = (e.clientY - rect.top) / k;
    let a = (Math.atan2(py - cy, px - cx) * 180) / Math.PI + 90; // ручка сверху = 0°
    if (a > 180) a -= 360;
    if (e.shiftKey) a = Math.round(a / 15) * 15;
    t.rotationZ = Math.round(a * 10) / 10;
    touch(o);
  });
}

// ---------- selection ----------

function select(id: string | null) {
  selectedId = id;
  renderOverlay();
  renderLayers();
  renderProps();
}

$("viewport").addEventListener("pointerdown", (e) => {
  if (!(e.target as HTMLElement).closest(".box")) select(null);
});

// ---------- layouts panel ----------

function renderLayouts() {
  const list = $("layoutList");
  list.replaceChildren();
  for (const l of layouts) {
    const item = el("div", { class: `item${l.id === layout?.id ? " sel" : ""}` });
    const name = el("span", { class: "name", textContent: l.name });
    name.title = l.scenes.length ? `OBS scenes: ${l.scenes.join(", ")}` : "No OBS scenes bound";
    item.append(name);
    if (l.id === defaultLayoutId) item.append(el("span", { class: "badge", textContent: "★ default", title: "Shown for scenes that have no layout of their own" }));
    else if (l.scenes.length) item.append(el("span", { class: "badge", textContent: String(l.scenes.length), title: l.scenes.join(", ") }));
    if (l.id === activeLayoutId) item.append(el("span", { class: "badge ok", textContent: "live", title: "Shown in /overlay now" }));
    const play = el("button", { class: "ib", textContent: "▶", title: "Show this layout in /overlay now" });
    play.addEventListener("click", (e) => {
      e.stopPropagation();
      wsSend({ type: "layout:activate", layoutId: l.id });
    });
    item.append(play);
    item.addEventListener("click", () => {
      if (l.id !== layout?.id) openLayout(l.id).catch(fail);
    });
    list.append(item);
  }

  const sel = $<HTMLSelectElement>("layoutSelect");
  sel.replaceChildren(...layouts.map((l) => el("option", { value: l.id, textContent: l.name, selected: l.id === layout?.id })));
  $("delLayoutBtn").toggleAttribute("disabled", layouts.length <= 1);
}

$<HTMLSelectElement>("layoutSelect").addEventListener("change", (e) => {
  openLayout((e.target as HTMLSelectElement).value).catch(fail);
});

$("newLayoutBtn").addEventListener("click", async () => {
  const name = prompt("Layout name (will also be used as OBS scene name):", "New Layout");
  if (!name) return;
  try {
    const l = await api<Layout>("/api/layouts", "POST", { name });
    await loadLayouts();
    await openLayout(l.id);
  } catch (e) { fail(e); }
});

$("dupLayoutBtn").addEventListener("click", async () => {
  if (!layout) return;
  const name = prompt("Name for the copy:", `${layout.name} copy`);
  if (!name) return;
  try {
    // дублируем сохранённую версию на сервере
    const l = await api<Layout>("/api/layouts", "POST", { name, duplicateOf: layout.id });
    await loadLayouts();
    await openLayout(l.id, true);
  } catch (e) { fail(e); }
});

$("delLayoutBtn").addEventListener("click", async () => {
  if (!layout || layouts.length <= 1) return;
  if (!confirm(`Delete layout "${layout.name}"?`)) return;
  try {
    await api(`/api/layouts/${encodeURIComponent(layout.id)}`, "DELETE");
    await loadLayouts();
    await openLayout(layouts[0].id, true);
  } catch (e) { fail(e); }
});

// ---------- layers panel ----------

let dragLayerId: string | null = null;

function renderLayers() {
  const list = $("layerList");
  list.replaceChildren();
  if (!layout) return;
  const objs = [...layout.objects].sort((a, b) => b.zIndex - a.zIndex); // верхний слой сверху
  if (objs.length === 0) list.append(el("div", { class: "empty", textContent: "No objects. Click “+ Add Source”." }));

  for (const o of objs) {
    const item = el("div", { class: `item${o.id === selectedId ? " sel" : ""}${o.visible ? "" : " hidden"}` });
    item.draggable = true;

    const eye = el("button", { class: "ib", textContent: o.visible ? "👁" : "◌", title: "Visibility" });
    eye.addEventListener("click", (e) => {
      e.stopPropagation();
      o.visible = !o.visible;
      markDirty();
      refreshCanvas();
      renderLayers();
    });
    const lock = el("button", { class: "ib", textContent: o.locked ? "🔒" : "🔓", title: "Lock" });
    lock.addEventListener("click", (e) => {
      e.stopPropagation();
      o.locked = !o.locked;
      markDirty();
      renderOverlay();
      renderLayers();
    });
    const name = el("span", { class: "name", textContent: objName(o), title: "Double-click to rename this layer (the source keeps its name)" });
    name.addEventListener("dblclick", (e) => {
      e.stopPropagation();
      const n = prompt("Layer name:", objName(o));
      if (n === null || !n.trim() || n.trim() === objName(o)) return;
      o.name = n.trim();
      markDirty();
      renderLayers();
    });
    const del = el("button", { class: "ib", textContent: "✕", title: "Remove from layout" });
    del.addEventListener("click", (e) => {
      e.stopPropagation();
      removeObject(o.id);
    });

    item.append(eye, lock, name, del);
    item.addEventListener("click", () => select(o.id));

    item.addEventListener("dragstart", (e) => {
      dragLayerId = o.id;
      e.dataTransfer?.setData("text/plain", o.id);
    });
    item.addEventListener("dragover", (e) => {
      if (!dragLayerId) return;
      e.preventDefault();
      item.classList.add("drop-before");
    });
    item.addEventListener("dragleave", () => item.classList.remove("drop-before"));
    item.addEventListener("drop", (e) => {
      e.preventDefault();
      item.classList.remove("drop-before");
      if (dragLayerId && dragLayerId !== o.id) reorder(dragLayerId, o.id);
      dragLayerId = null;
    });
    item.addEventListener("dragend", () => { dragLayerId = null; });
    list.append(item);
  }
}

/** Переносит `movedId` непосредственно над `beforeId` в отображаемом порядке (верх = больший zIndex). */
function reorder(movedId: string, beforeId: string) {
  if (!layout) return;
  const order = [...layout.objects].sort((a, b) => b.zIndex - a.zIndex).map((o) => o.id);
  order.splice(order.indexOf(movedId), 1);
  order.splice(order.indexOf(beforeId), 0, movedId);
  const n = order.length;
  order.forEach((id, i) => {
    const o = layout!.objects.find((x) => x.id === id)!;
    o.zIndex = n - i;
  });
  markDirty();
  refreshCanvas();
  renderLayers();
}

function removeObject(id: string) {
  if (!layout) return;
  layout.objects = layout.objects.filter((o) => o.id !== id);
  if (selectedId === id) selectedId = null;
  markDirty();
  refreshCanvas();
  renderLayers();
  renderProps();
}

// ---------- copy between layouts ----------

/**
 * Гарантирует у слоя instanceId (нужен для связи копий между layout). Если он был назначен сейчас, а несохранённых правок
 * не было — сразу сохраняем layout, чтобы связь работала; иначе предупреждаем, что layout надо сохранить.
 */
async function ensureInstanceId(o: LayoutObject) {
  if (o.instanceId) return;
  const wasDirty = dirty;
  o.instanceId = uid("inst");
  markDirty();
  if (wasDirty) toast("Save this layout (Ctrl+S) so the copy stays linked");
  else await save();
}

/** Копия слоя для layout `target`. linked: тот же instanceId (если в target его ещё нет) — виджет не перезагружается при смене сцены. */
function cloneObject(o: LayoutObject, target: Layout, opts: { linked: boolean; offset?: number; copySuffix?: boolean }): LayoutObject {
  const c = JSON.parse(JSON.stringify(o)) as LayoutObject;
  c.id = uid("obj");
  const canLink = opts.linked && !!o.instanceId && !target.objects.some((x) => x.instanceId === o.instanceId);
  c.instanceId = canLink ? o.instanceId : uid("inst");
  c.zIndex = Math.max(0, ...target.objects.map((x) => x.zIndex)) + 1;
  c.transform.x += opts.offset ?? 0;
  c.transform.y += opts.offset ?? 0;
  if (opts.copySuffix) c.name = `${objName(o)} (copy)`;
  return c;
}

const copyDialog = $<HTMLDialogElement>("copyDialog");

function openCopyDialog() {
  const o = selected();
  if (!layout || !o) return toast("Select a layer first");
  const others = layouts.filter((l) => l.id !== layout!.id);
  if (others.length === 0) return toast("Create another layout first");
  $("copyWhat").textContent = `Layer “${objName(o)}” from “${layout.name}”`;
  $<HTMLSelectElement>("copyTarget").replaceChildren(...others.map((l) => el("option", { value: l.id, textContent: l.name })));
  copyDialog.showModal();
}

$("copyObjBtn").addEventListener("click", openCopyDialog);

$("copyGoBtn").addEventListener("click", async () => {
  const o = selected();
  const targetId = $<HTMLSelectElement>("copyTarget").value;
  if (!layout || !o || !targetId) return;
  const linked = $<HTMLInputElement>("copyLinked").checked;
  copyDialog.close();
  try {
    if (linked) await ensureInstanceId(o);
    const target = await api<Layout>(`/api/layouts/${encodeURIComponent(targetId)}`);
    target.objects.push(cloneObject(o, target, { linked }));
    await api(`/api/layouts/${encodeURIComponent(targetId)}`, "PUT", target);
    await loadLayouts();
    renderLayouts();
    toast(`Copied to “${target.name}”`);
  } catch (e) { fail(e); }
});

async function copySelected() {
  const o = selected();
  if (!layout || !o) return;
  await ensureInstanceId(o);
  clip = { obj: JSON.parse(JSON.stringify(o)) as LayoutObject, fromLayoutId: layout.id };
  toast(`Copied “${objName(o)}” — Ctrl+V pastes it into the open layout`);
}

function pasteClip() {
  if (!layout || !clip) return;
  const same = clip.fromLayoutId === layout.id;
  // В тот же layout — независимая копия со сдвигом; в другой — связанная (тот же виджет).
  const c = cloneObject(clip.obj, layout, { linked: !same, offset: same ? 20 : 0, copySuffix: same });
  layout.objects.push(c);
  selectedId = c.id;
  markDirty();
  refreshCanvas();
  renderLayers();
  renderProps();
}

// ---------- add source ----------

const addDialog = $<HTMLDialogElement>("addDialog");

type AddTab = "iframe" | "existing" | "plugin";
let selectedPluginId = "";
let selectedExistingId = "";

function setAddTab(tab: AddTab) {
  if (tab === "existing" && sources.length === 0) tab = "iframe";
  for (const b of document.querySelectorAll<HTMLButtonElement>("#addDialog .tab")) {
    const on = b.dataset.tab === tab;
    b.classList.toggle("sel", on);
    b.setAttribute("aria-selected", String(on));
  }
  for (const t of ["iframe", "existing", "plugin"]) $(`pane-${t}`).hidden = t !== tab;
  if (tab === "iframe") $("newName").focus();
}
for (const b of document.querySelectorAll<HTMLButtonElement>("#addDialog .tab")) {
  b.addEventListener("click", () => setAddTab(b.dataset.tab as AddTab));
}

function pickItem(sel: boolean, title: string, meta: string, onPick: () => void, onDouble?: () => void): HTMLElement {
  const it = el("div", { class: `item${sel ? " sel" : ""}`, tabIndex: 0 },
    el("div", { style: "flex:1;min-width:0" }, el("div", { class: "name", textContent: title }), el("div", { class: "meta", textContent: meta })));
  it.addEventListener("click", onPick);
  it.addEventListener("keydown", (e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); onPick(); } });
  if (onDouble) it.addEventListener("dblclick", onDouble);
  return it;
}

/** В списках не показываем всё после «?» (и «#»): в адресах виджетов часто лежат токены и секреты. */
function maskUrl(u: string): string {
  const i = u.search(/[?#]/);
  return i < 0 ? u : `${u.slice(0, i + 1)}${"•".repeat(8)}`;
}

function renderExistingList() {
  const list = $("existingList");
  if (!sources.some((x) => x.id === selectedExistingId)) selectedExistingId = sources[0]?.id ?? "";
  list.replaceChildren(...sources.map((x) => pickItem(
    x.id === selectedExistingId, x.name, x.type === "plugin" ? `plugin · ${x.data.plugin}` : maskUrl(x.data.url ?? "iframe"),
    () => { selectedExistingId = x.id; renderExistingList(); },
    () => $("useExistingBtn").click(),
  )));
  if (sources.length === 0) list.replaceChildren(el("div", { class: "empty", textContent: "No sources yet — create one on the first tab." }));
  $("useExistingBtn").toggleAttribute("disabled", sources.length === 0);
  $("tabExisting").toggleAttribute("disabled", sources.length === 0);
}

function renderPluginPicker() {
  const list = $("pluginList");
  if (!plugins.some((x) => x.id === selectedPluginId)) selectedPluginId = plugins[0]?.id ?? "";
  list.replaceChildren(...plugins.map((p) => pickItem(
    p.id === selectedPluginId,
    `${p.name}${p.version ? ` ${p.version}` : ""}${p.builtin ? "" : " (custom)"}`,
    p.description ?? "",
    () => { selectedPluginId = p.id; renderPluginPicker(); },
    () => $("usePluginBtn").click(),
  )));
  if (plugins.length === 0) {
    list.replaceChildren(el("div", { class: "empty", textContent: "No plugins installed yet. Open the Plugin store to install some, or put a plugin folder (with plugin.json) into the plugins folder." }));
  }
  const p = plugins.find((x) => x.id === selectedPluginId);
  $("pluginHint").textContent = !p ? "" : p.permissions?.length
    ? `Asks for: ${p.permissions.map((x) => PERM_TEXT[x] ?? x).join("; ")}. You confirm this when placing it.`
    : "Asks for no permissions.";
  $("usePluginBtn").toggleAttribute("disabled", !p);
}

$("openStoreBtn").addEventListener("click", () => { storeDialog.showModal(); void loadStore(); });

// ---------- plugin store ----------

type StoreEntry = {
  id: string; name: string; version?: string; description?: string; author?: string; permissions?: string[]; repo: string;
  installed: boolean; installedVersion?: string; installedCommit?: string; updateAvailable: boolean; blocked?: "builtin" | "foreign";
  digest: string; commit?: string; addedPermissions?: string[];
};
type StoreSource = { repo: string; error?: string; plugins: StoreEntry[] };
type StoreData = { sources: StoreSource[]; installed: Record<string, { repo: string; version?: string }>; hasToken: boolean };

const storeDialog = $<HTMLDialogElement>("storeDialog");
const BLOCKED_TEXT = { builtin: "built in", foreign: "id is taken by another plugin" };

async function loadStore(refresh = false) {
  const body = $("storeBody");
  body.replaceChildren(el("div", { class: "empty", textContent: "Loading…" }));
  try {
    renderStore(await api<StoreData>(`/api/market${refresh ? "?refresh=1" : ""}`));
  } catch (e) {
    body.replaceChildren(el("div", { class: "hint warn", textContent: `Store is unavailable: ${e instanceof Error ? e.message : e}` }));
  }
}

/** Плагин поменялся на диске: подхватить в редакторе (список для Add Source, виджеты на холсте). */
async function afterPluginChange() {
  await loadPlugins().catch(fail);
  renderPluginPicker();
  await loadSources().catch(fail);
  refreshCanvas();
  renderLayers();
  renderProps();
}

function renderStore(d: StoreData) {
  const body = $("storeBody");
  $("storeTokenSummary").textContent = d.hasToken ? "Private repositories — GitHub token is set" : "Private repositories (GitHub token)";
  $<HTMLButtonElement>("storeTokenClear").toggleAttribute("disabled", !d.hasToken);
  const shown = new Set<string>();
  const parts: HTMLElement[] = [];
  const act = (btn: HTMLButtonElement, fn: () => Promise<unknown>) => {
    btn.addEventListener("click", async () => {
      btn.disabled = true;
      try { await fn(); } catch (e) { fail(e); }
      await loadStore();
    });
    return btn;
  };
  for (const s of d.sources) {
    const rm = act(el("button", { type: "button", textContent: "Remove source", title: "Plugins already installed from it stay" }), async () => {
      if (confirm(`Remove source ${s.repo}? Plugins already installed from it stay installed.`)) await api(`/api/market/sources?repo=${encodeURIComponent(s.repo)}`, "DELETE");
    });
    parts.push(el("div", { class: "row", style: "justify-content:space-between;margin-top:12px" }, el("strong", { textContent: s.repo }), rm));
    if (s.error) parts.push(el("div", { class: "hint warn", textContent: s.error }));
    else if (!s.plugins.length) parts.push(el("div", { class: "empty", textContent: "No plugins in this repository." }));
    for (const p of s.plugins) {
      if (p.installed) shown.add(p.id);
      const used = sources.filter((x) => x.type === "plugin" && x.data.plugin === p.id).length;
      const perms = p.permissions?.length ? `Asks for: ${p.permissions.map((x) => PERM_TEXT[x] ?? x).join("; ")}` : "Asks for no permissions";
      let button: HTMLElement;
      if (p.blocked) button = el("span", { class: "hint", textContent: BLOCKED_TEXT[p.blocked] });
      else if (p.updateAvailable) button = act(el("button", { type: "button", class: "primary", textContent: p.version && p.version !== p.installedVersion ? `Update to ${p.version}` : "Update (files changed)" }), async () => {
        // Новые права при обновлении — отдельное подтверждение: молча расширить доступ плагина нельзя.
        if (p.addedPermissions?.length && !confirm(`The new version of “${p.name}” asks for additional permissions:\n\n${p.addedPermissions.map((x) => `• ${PERM_TEXT[x] ?? x}`).join("\n")}\n\nUpdate anyway? (Widgets keep only the permissions you already allowed; allow the new ones in the widget properties.)`)) return;
        await api("/api/market/install", "POST", { repo: p.repo, id: p.id, digest: p.digest });
        await afterPluginChange();
        toast(`${p.name} updated. Reload the overlay page in OBS to apply.`);
      });
      else if (p.installed) button = act(el("button", { type: "button", class: "danger", textContent: "Uninstall" }), async () => {
        const msg = used ? `Uninstall “${p.name}”? ${used} widget(s) using it will become empty. Its stored data is kept (erase it in widget properties).` : `Uninstall “${p.name}”?`;
        if (!confirm(msg)) return;
        await api("/api/market/uninstall", "POST", { id: p.id });
        await afterPluginChange();
      });
      else button = act(el("button", { type: "button", class: "primary", textContent: "Install" }), async () => {
        await api("/api/market/install", "POST", { repo: p.repo, id: p.id, digest: p.digest });
        await afterPluginChange();
        toast(`${p.name} installed. Add it via Add Source → widget plugin.`);
      });
      const src = p.commit ? ` · commit ${p.commit}` : "";
      const title = `${p.name}${p.version ? ` ${p.version}` : ""}${src}${p.installed && p.installedVersion && p.installedVersion !== p.version ? ` (installed ${p.installedVersion})` : p.installed ? " — installed" : ""}`;
      parts.push(el("div", { class: "row", style: "align-items:flex-start;justify-content:space-between;border-top:1px solid var(--line);padding-top:6px" },
        el("div", { style: "flex:1;min-width:0" },
          el("div", { textContent: title }),
          el("div", { class: "hint", textContent: [p.author, p.description].filter(Boolean).join(" — ") }),
          el("div", { class: "hint", textContent: perms })),
        button));
    }
  }
  // Поставленные из магазина, но источник уже убран: управлять ими всё равно надо.
  const orphans = Object.entries(d.installed).filter(([id]) => !shown.has(id));
  if (orphans.length) {
    parts.push(el("div", { style: "margin-top:12px" }, el("strong", { textContent: "Installed from sources no longer in the list" })));
    for (const [id, rec] of orphans) {
      const name = plugins.find((x) => x.id === id)?.name ?? id;
      const b = act(el("button", { type: "button", class: "danger", textContent: "Uninstall" }), async () => {
        if (!confirm(`Uninstall “${name}”?`)) return;
        await api("/api/market/uninstall", "POST", { id });
        await afterPluginChange();
      });
      parts.push(el("div", { class: "row", style: "justify-content:space-between;border-top:1px solid var(--line);padding-top:6px" },
        el("div", {}, el("div", { textContent: `${name}${rec.version ? ` ${rec.version}` : ""}` }), el("div", { class: "hint", textContent: rec.repo })), b));
    }
  }
  if (!d.sources.length && !orphans.length) parts.push(el("div", { class: "empty", textContent: "No sources yet. Add a GitHub repository above." }));
  body.replaceChildren(...parts);
}

$("storeRefreshBtn").addEventListener("click", () => void loadStore(true));
$("storeAddBtn").addEventListener("click", async () => {
  const inp = $<HTMLInputElement>("storeRepo");
  if (!inp.value.trim()) return;
  const btn = $<HTMLButtonElement>("storeAddBtn");
  btn.disabled = true;
  try { await api("/api/market/sources", "POST", { repo: inp.value }); inp.value = ""; } catch (e) { fail(e); }
  btn.disabled = false;
  await loadStore();
});
$("storeTokenSave").addEventListener("click", async () => {
  const inp = $<HTMLInputElement>("storeToken");
  if (!inp.value.trim()) return;
  try { await api("/api/market/token", "PUT", { token: inp.value }); inp.value = ""; toast("GitHub token saved"); } catch (e) { fail(e); }
  await loadStore(true);
});
$("storeTokenClear").addEventListener("click", async () => {
  if (!confirm("Remove the saved GitHub token? Private repositories will stop loading.")) return;
  try { await api("/api/market/token", "DELETE"); } catch (e) { fail(e); }
  await loadStore(true);
});
$("storeRepo").addEventListener("keydown", (e) => { if (e.key === "Enter") { e.preventDefault(); $("storeAddBtn").click(); } });

$("openPluginsBtn").addEventListener("click", () => api("/api/app/open", "POST", { what: "plugins" }).catch(fail));

const PERM_TEXT: Record<string, string> = {
  "chat.read": "Read Twitch chat",
  "chat.send": "Write to Twitch chat as your bot / streamer account",
  "net.local": "Read data from programs running on this computer (e.g. tosu)",
  "storage": "Store this plugin’s data on this computer",
};

$("usePluginBtn").addEventListener("click", async () => {
  const p = plugins.find((x) => x.id === selectedPluginId);
  if (!p) return;
  // Разрешения выдаёт пользователь: плагин — чужой код, а чат и аккаунты Twitch — его данные.
  let granted: string[] = [];
  if (p.permissions?.length) {
    const list = p.permissions.map((x) => `• ${PERM_TEXT[x] ?? x}`).join("\n");
    if (confirm(`“${p.name}” asks for permissions:\n\n${list}\n\nAllow? (You can change this later in the widget properties.)`)) granted = p.permissions;
  }
  try {
    let name = p.name, n = 2;
    while (sources.some((x) => x.name === name)) name = `${p.name} ${n++}`;
    const id = uid("src");
    await api("/api/sources", "POST", { id, name, type: "plugin", data: { plugin: p.id, settings: {}, granted } });
    await loadSources();
    placeObject(id, p.defaultSize.width, p.defaultSize.height);
    addDialog.close();
  } catch (e) { fail(e); }
});

$("addObjBtn").addEventListener("click", async () => {
  await loadPlugins().catch(fail);
  renderPluginPicker();
  renderExistingList();
  addDialog.showModal();
  setAddTab("iframe"); // по умолчанию — обычная страница по адресу
});

for (const id of ["newName", "newUrl"]) {
  $(id).addEventListener("keydown", (e) => { if ((e as KeyboardEvent).key === "Enter") { e.preventDefault(); $("createBtn").click(); } });
}

function placeObject(sourceId: string, w = 400, h = 300) {
  if (!layout) return;
  const maxZ = Math.max(0, ...layout.objects.map((o) => o.zIndex));
  const t = { ...defaultTransform(), width: w, height: h };
  t.x = Math.round((layout.canvas.width - w) / 2);
  t.y = Math.round((layout.canvas.height - h) / 2);
  const obj: LayoutObject = {
    id: uid("obj"), name: sourceOf(sourceId)?.name, sourceId, transform: t, opacity: 1, visible: true, locked: false, zIndex: maxZ + 1,
  };
  layout.objects.push(obj);
  selectedId = obj.id;
  markDirty();
  refreshCanvas();
  renderLayers();
  renderProps();
}

$("useExistingBtn").addEventListener("click", () => {
  if (selectedExistingId) placeObject(selectedExistingId);
  addDialog.close();
});

$("createBtn").addEventListener("click", async () => {
  const name = $<HTMLInputElement>("newName").value.trim();
  const url = $<HTMLInputElement>("newUrl").value.trim();
  const proxy = $<HTMLInputElement>("newProxy").checked;
  const w = Math.max(MIN_SIZE, Number($<HTMLInputElement>("newW").value) || 400);
  const h = Math.max(MIN_SIZE, Number($<HTMLInputElement>("newH").value) || 300);
  if (!name || !url) return toast("Name and URL are required");
  try {
    const id = uid("src");
    await api("/api/sources", "POST", { id, name, type: "iframe", data: proxy ? { url, proxy: true } : { url } });
    await loadSources();
    placeObject(id, w, h);
    addDialog.close();
    $<HTMLInputElement>("newName").value = "";
    $<HTMLInputElement>("newUrl").value = "";
    $<HTMLInputElement>("newProxy").checked = false;
  } catch (e) { fail(e); }
});

// ---------- properties panel ----------

function numField(label: string, get: () => number, set: (v: number) => void, step = 1): HTMLLabelElement {
  const input = el("input", { type: "number" });
  input.step = String(step);
  const sync = () => {
    if (document.activeElement !== input) input.value = String(Math.round(get() * 100) / 100);
  };
  sync();
  propSync.push(sync);
  input.addEventListener("input", () => {
    const v = parseFloat(input.value);
    if (Number.isNaN(v)) return;
    set(v);
    markDirty();
    scene.setLayout(layout!);
    renderOverlay();
    propSync.forEach((f) => f());
  });
  return el("label", {}, label, input);
}

// ---------- plugin settings form ----------

const COMMON_FONTS = ["Segoe UI", "Arial", "Verdana", "Tahoma", "Trebuchet MS", "Georgia", "Times New Roman", "Courier New", "Consolas",
  "Impact", "Comic Sans MS", "Roboto", "Open Sans", "Montserrat", "Noto Sans", "system-ui"];
{
  const dl = el("datalist", { id: "fontList" }, ...COMMON_FONTS.map((f) => el("option", { value: f })));
  document.body.append(dl);
}

type SettingValue = string | number | boolean;

/** Форма настроек плагина-виджета по схеме из plugin.json. Каждое изменение сохраняется сразу (виджет перезагрузится с новыми query). */
function pluginSettings(src: Source, m: PluginManifest): HTMLElement {
  const root = el("div");
  const save = async (key: string, v: SettingValue | undefined) => {
    const next: Record<string, SettingValue> = { ...(src.data.settings ?? {}) };
    if (v === undefined) delete next[key]; else next[key] = v;
    try {
      const r = await api<Source>(`/api/sources/${encodeURIComponent(src.id)}`, "PUT", { data: { settings: next } });
      src.data.settings = r.data.settings ?? {};
    } catch (e) { fail(e); renderProps(); }
  };

  if (m.permissions?.length) {
    root.append(el("h4", { class: "group", textContent: "Permissions" }));
    for (const perm of m.permissions) {
      const chk = el("input", { type: "checkbox", checked: src.data.granted?.includes(perm) ?? false });
      chk.addEventListener("change", async () => {
        const set = new Set(src.data.granted ?? []);
        if (chk.checked) set.add(perm); else set.delete(perm);
        try {
          const r = await api<Source>(`/api/sources/${encodeURIComponent(src.id)}`, "PUT", { data: { granted: [...set] } });
          src.data.granted = r.data.granted ?? [];
        } catch (e) { fail(e); renderProps(); }
      });
      root.append(el("label", { class: "chk" }, chk, ` ${PERM_TEXT[perm] ?? perm}`));
    }
    root.append(el("div", { class: "hint", textContent: "Changing permissions reloads the widget." }));
    if (m.permissions.includes("storage")) root.append(pluginDataRow(m.id));
  }

  let group: string | undefined;
  for (const s of m.settings ?? []) {
    if (s.group !== group) {
      group = s.group;
      if (group) root.append(el("h4", { class: "group", textContent: group }));
    }
    root.append(settingControl(s, src.data.settings?.[s.key], save));
  }
  if (!m.settings?.length) root.append(el("div", { class: "empty", textContent: "This widget has no settings." }));
  return root;
}

/** Сколько данных плагин хранит (oc.storage) и кнопка «стереть» — данные общие для всех виджетов этого плагина. */
function pluginDataRow(pluginId: string): HTMLElement {
  const info = el("span", { class: "hint", textContent: "Plugin data: …" });
  const erase = el("button", { type: "button", textContent: "Erase data" });
  const refresh = async () => {
    try {
      const st = await api<{ keys: number; bytes: number }>(`/api/plugins/${encodeURIComponent(pluginId)}/storage`);
      info.textContent = `Plugin data: ${st.keys} keys, ${(st.bytes / 1024).toFixed(1)} KB`;
      erase.toggleAttribute("disabled", st.keys === 0);
    } catch { info.textContent = "Plugin data: unavailable"; }
  };
  erase.addEventListener("click", async () => {
    if (!confirm("Erase all data stored by this plugin? This cannot be undone.")) return;
    try { await api(`/api/plugins/${encodeURIComponent(pluginId)}/storage`, "DELETE"); await refresh(); } catch (e) { fail(e); }
  });
  refresh();
  return el("div", { class: "row", style: "justify-content:space-between" }, info, erase);
}

function settingControl(s: PluginSetting, current: SettingValue | undefined, save: (key: string, v: SettingValue | undefined) => void): HTMLElement {
  const val = current ?? s.default;
  const line = el("div", { class: "line" });
  let input: HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement;

  switch (s.type) {
    case "bool": {
      const chk = el("input", { type: "checkbox", checked: Boolean(val) });
      chk.addEventListener("change", () => save(s.key, chk.checked));
      input = chk;
      break;
    }
    case "number":
    case "range": {
      const num = el("input", { type: s.type === "range" ? "range" : "number" });
      if (s.min !== undefined) num.min = String(s.min);
      if (s.max !== undefined) num.max = String(s.max);
      num.step = String(s.step ?? 1);
      num.value = String(val ?? s.min ?? 0);
      const shown = el("span", { textContent: s.type === "range" ? String(num.value) : "" });
      num.addEventListener("input", () => (shown.textContent = s.type === "range" ? num.value : ""));
      num.addEventListener("change", () => {
        const v = parseFloat(num.value);
        if (!Number.isNaN(v)) save(s.key, v);
      });
      input = num;
      if (s.type === "range") line.append(num, shown);
      break;
    }
    case "color": {
      const col = el("input", { type: "color", value: typeof val === "string" && /^#[0-9a-f]{6}$/i.test(val) ? val : "#000000" });
      col.addEventListener("change", () => save(s.key, col.value));
      input = col;
      break;
    }
    case "select": {
      const sel = el("select", {}, ...(s.options ?? []).map((o) => el("option", { value: o.value, textContent: o.label, selected: o.value === val })));
      sel.addEventListener("change", () => save(s.key, sel.value));
      input = sel;
      break;
    }
    case "text": {
      const ta = el("textarea", { value: String(val ?? ""), rows: 3 });
      ta.addEventListener("change", () => save(s.key, ta.value));
      input = ta;
      break;
    }
    default: { // string, font
      const txt = el("input", { type: "text", value: String(val ?? "") });
      if (s.type === "font") txt.setAttribute("list", "fontList");
      txt.addEventListener("change", () => save(s.key, txt.value));
      input = txt;
    }
  }

  const isBool = s.type === "bool";
  if (!(s.type === "range")) line.append(input);
  const wrap = el("div", { class: "setting" });
  if (isBool) {
    wrap.append(el("div", { class: "line" }, el("label", { class: "chk", style: "margin:0" }, input, s.label)));
  } else {
    wrap.append(el("span", { textContent: s.label }), line);
  }
  if (current !== undefined) { // значение отличается от умолчания → можно сбросить
    const reset = el("button", { class: "ib", textContent: "↺", title: "Reset to default" });
    reset.addEventListener("click", () => save(s.key, undefined));
    (isBool ? wrap.firstElementChild! : line).append(reset);
  }
  if (s.help) wrap.append(el("div", { class: "hint", textContent: s.help }));
  return wrap;
}

function renderProps() {
  const root = $("props");
  root.replaceChildren();
  propSync = [];
  if (!layout) return;

  const o = selected();
  if (o) {
    const t = o.transform;
    const src = sourceOf(o.sourceId);
    root.append(el("h3", { textContent: "Source" }));
    if (src?.type === "plugin") {
      const name = el("input", { type: "text", value: src.name });
      name.addEventListener("change", () => api(`/api/sources/${src.id}`, "PUT", { name: name.value }).catch(fail));
      root.append(el("div", { class: "row" }, name));
      const m = plugins.find((p) => p.id === src.data.plugin);
      if (m) {
        root.append(el("div", { class: "hint", textContent: `${m.name}${m.version ? ` ${m.version}` : ""}${m.builtin ? "" : " · custom plugin"}` }));
        root.append(pluginSettings(src, m));
      } else {
        root.append(el("div", { class: "hint warn", textContent: `Plugin “${src.data.plugin}” is not installed — the widget will be empty. Put it back into the plugins folder.` }));
      }
    } else if (src) {
      const name = el("input", { type: "text", value: src.name });
      name.addEventListener("change", () => api(`/api/sources/${src.id}`, "PUT", { name: name.value }).catch(fail));
      // В адресе виджета часто лежит токен: всё после «?»/«#» скрыто, поле открывается на редактирование двойным кликом,
      // а скопировать адрес целиком можно кнопкой рядом.
      let curUrl = src.data.url ?? "";
      const url = el("input", { type: "text" });
      const paintUrl = () => {
        const masked = maskUrl(curUrl) !== curUrl;
        url.value = masked ? maskUrl(curUrl) : curUrl;
        url.readOnly = masked;
        url.title = masked ? "Part of the address is hidden. Double-click to edit; the button copies the full address." : "";
      };
      paintUrl();
      url.addEventListener("dblclick", () => {
        if (!url.readOnly) return;
        url.readOnly = false;
        url.value = curUrl;
        url.focus();
      });
      url.addEventListener("change", () => {
        if (url.readOnly) return;
        curUrl = url.value;
        api(`/api/sources/${src.id}`, "PUT", { data: { url: curUrl } }).catch(fail);
      });
      url.addEventListener("blur", paintUrl);
      const copyUrl = el("button", { type: "button", class: "iconbtn", title: "Copy the full address", ariaLabel: "Copy the full address" });
      copyUrl.innerHTML = COPY_ICON; // статическая разметка без пользовательских данных
      copyUrl.addEventListener("click", async () => {
        toast((await copyText(curUrl)) ? "Address copied" : "Could not copy — select the address manually");
      });
      const proxyChk = el("input", { type: "checkbox", checked: !!src.data.proxy });
      proxyChk.addEventListener("change", () =>
        api(`/api/sources/${src.id}`, "PUT", { data: { proxy: proxyChk.checked } }).catch((e) => {
          proxyChk.checked = !proxyChk.checked; // сервер отказал (например, относительный URL)
          fail(e);
        }));
      root.append(
        el("div", { class: "row" }, name),
        el("div", { class: "row" }, url, copyUrl),
        el("label", { class: "chk", title: "For sites that refuse to be embedded (X-Frame-Options)" }, proxyChk, "Load via server proxy"),
      );
    }

    root.append(el("h3", { textContent: "Transform" }));
    const g = el("div", { class: "grid2" });
    g.append(
      numField("X", () => t.x, (v) => (t.x = v)),
      numField("Y", () => t.y, (v) => (t.y = v)),
      numField("Width", () => t.width, (v) => (t.width = Math.max(1, v, (t.cropLeft ?? 0) + (t.cropRight ?? 0) + 1))),
      numField("Height", () => t.height, (v) => (t.height = Math.max(1, v, (t.cropTop ?? 0) + (t.cropBottom ?? 0) + 1))),
      numField("Rotation °", () => t.rotationZ, (v) => (t.rotationZ = v), 0.5),
      numField("Opacity %", () => o.opacity * 100, (v) => (o.opacity = Math.min(1, Math.max(0, v / 100))), 5),
      numField("Scale X", () => t.scaleX, (v) => (t.scaleX = v), 0.05),
      numField("Scale Y", () => t.scaleY, (v) => (t.scaleY = v), 0.05),
    );
    root.append(g);

    root.append(el("h3", { textContent: "Crop (px)" }));
    const cropGrid = el("div", { class: "grid2" });
    cropGrid.append(
      cropField("Left", t, "cropLeft"), cropField("Right", t, "cropRight"),
      cropField("Top", t, "cropTop"), cropField("Bottom", t, "cropBottom"),
    );
    const resetCrop = el("button", { textContent: "Reset crop" });
    resetCrop.addEventListener("click", () => {
      const f = contentCenter(t);
      t.cropLeft = t.cropRight = t.cropTop = t.cropBottom = 0;
      placeByContentCenter(t, f);
      touch(o);
      renderProps();
    });
    root.append(cropGrid, el("div", { class: "row" }, resetCrop), el("div", { class: "empty", textContent: "Alt+drag a handle crops, like in OBS." }));

    const rm = el("button", { textContent: "Remove from layout" });
    rm.addEventListener("click", () => removeObject(o.id));
    const delSrc = el("button", { class: "danger", textContent: "Delete source" });
    delSrc.addEventListener("click", async () => {
      if (!src || !confirm(`Delete source "${src.name}" from ALL layouts?`)) return;
      try {
        await api(`/api/sources/${src.id}`, "DELETE");
        // сервер вычистил объекты в сохранённых layout; убираем и из рабочей копии
        layout!.objects = layout!.objects.filter((x) => x.sourceId !== src.id);
        selectedId = null;
        await loadSources();
        refreshAll();
      } catch (e) { fail(e); }
    });
    root.append(el("div", { class: "row", style: "margin-top:10px" }, rm, delSrc));
  }

  // layout settings
  root.append(el("h3", { textContent: "Layout" }));
  const lname = el("input", { type: "text", value: layout.name });
  lname.addEventListener("input", () => { layout!.name = lname.value; markDirty(); });
  const isDefault = el("input", { type: "checkbox", checked: layout.id === defaultLayoutId });
  isDefault.addEventListener("change", async () => {
    try {
      await api("/api/default-layout", "PUT", { layoutId: isDefault.checked ? layout!.id : null });
      await loadLayouts();
      renderLayouts();
      toast(isDefault.checked ? "Default layout set" : "No default layout");
    } catch (e) { isDefault.checked = !isDefault.checked; fail(e); }
  });
  const cw = el("input", { type: "number", value: String(layout.canvas.width) });
  const ch = el("input", { type: "number", value: String(layout.canvas.height) });
  const canvasChanged = () => {
    const w = Math.max(16, Number(cw.value) || 1920);
    const h = Math.max(16, Number(ch.value) || 1080);
    layout!.canvas = { width: w, height: h };
    markDirty();
    refreshCanvas();
  };
  cw.addEventListener("input", canvasChanged);
  ch.addEventListener("input", canvasChanged);
  root.append(
    el("label", {}, "Name", lname),
    el("label", { class: "chk", style: "margin-top:6px" }, isDefault, " Default layout (for scenes without their own)"),
    el("div", { id: "sceneBox", style: "margin-top:6px" }),
    el("div", { class: "grid2", style: "margin-top:6px" }, el("label", {}, "Canvas W", cw), el("label", {}, "Canvas H", ch)),
    el("div", { class: "empty", textContent: "Shift while resizing keeps aspect; Shift while rotating snaps to 15°. Arrows nudge (Shift = 10px)." }),
  );
  renderSceneBox();
}

// ---------- привязка layout к сценам OBS ----------

/** Блок «OBS scenes» в свойствах layout. Перерисовывается отдельно (список сцен и привязки приходят асинхронно). */
function renderSceneBox() {
  const box = document.getElementById("sceneBox");
  if (!box || !layout) return;
  const cur = layout;
  const owner = new Map<string, string>(); // сцена → имя другого layout, которому она принадлежит (сохранённое состояние)
  for (const l of layouts) if (l.id !== cur.id) for (const s of l.scenes) owner.set(s, l.name);

  // Сцены OBS + привязанные, которых в OBS сейчас нет (переименованы/удалены/OBS не подключён).
  const names = [...obsScenes];
  for (const s of cur.scenes) if (!names.includes(s)) names.push(s);

  const rows = names.map((s) => {
    const cb = el("input", { type: "checkbox", checked: cur.scenes.includes(s) });
    cb.addEventListener("change", () => {
      cur.scenes = cb.checked ? [...cur.scenes, s] : cur.scenes.filter((x) => x !== s);
      markDirty();
      renderSceneBox();
    });
    const notes: string[] = [];
    if (owner.has(s)) notes.push(cb.checked ? `moves from “${owner.get(s)}”` : `used by “${owner.get(s)}”`);
    if (obsScenes.length && !obsScenes.includes(s)) notes.push("not in OBS");
    return el("label", { class: "chk" }, cb, ` ${s}`, notes.length ? el("span", { class: "hint", textContent: ` — ${notes.join(", ")}` }) : "");
  });

  const add = el("input", { type: "text", placeholder: "Add scene by name…" });
  const addBtn = el("button", { type: "button", textContent: "Add" });
  const doAdd = () => {
    const v = add.value.trim();
    if (!v || cur.scenes.includes(v)) return;
    cur.scenes = [...cur.scenes, v];
    markDirty();
    renderSceneBox();
  };
  addBtn.addEventListener("click", doAdd);
  add.addEventListener("keydown", (e) => { if (e.key === "Enter") { e.preventDefault(); doAdd(); } });

  box.replaceChildren(
    el("div", { class: "hint", textContent: obsScenes.length
      ? "Show this layout when OBS switches to the checked scenes:"
      : "OBS scene list is not available yet (connect OBS in Settings). You can still add scene names by hand:" }),
    ...(rows.length ? rows : [el("div", { class: "empty", textContent: "No scenes bound — this layout is shown only by ▶ or as the default." })]),
    el("div", { class: "row", style: "margin-top:4px" }, add, addBtn),
  );
}

// ---------- settings (config.json) ----------

interface AppView {
  compiled: boolean;
  os: string;
  configFile: string;
  dataDir: string;
  logFile: string;
  autostart: boolean | null;
  tray: boolean;
  trayAvailable: boolean;
}

async function loadAppInfo() {
  try {
    const a = await api<AppView>("/api/app");
    $("cfgPaths").textContent = `Config: ${a.configFile}\nData: ${a.dataDir}\nLog: ${a.logFile}`;
    const cb = $<HTMLInputElement>("cfgAutostart");
    cb.checked = a.autostart === true;
    cb.disabled = a.autostart === null;
    $("cfgAutostartHint").textContent = a.autostart === null
      ? "Autostart is available in the packaged app (Windows, Linux)."
      : "Starts minimized (no browser window) when you log in.";
    $("cfgTrayHint").textContent = a.tray
      ? "The app lives in the system tray (right-click the icon)."
      : a.os === "windows" && a.compiled ? "Tray icon failed to start — see the log." : "";
  } catch { /* старый сервер */ }
}

$("cfgAutostart").addEventListener("change", async (e) => {
  const cb = e.target as HTMLInputElement;
  try {
    await api("/api/app/autostart", "PUT", { enabled: cb.checked });
  } catch (err) {
    cb.checked = !cb.checked;
    fail(err);
  }
});
$("cfgOpenData").addEventListener("click", () => api("/api/app/open", "POST", { what: "data" }).catch(fail));
$("cfgOpenLogs").addEventListener("click", () => api("/api/app/open", "POST", { what: "logs" }).catch(fail));
$("cfgQuit").addEventListener("click", async () => {
  if (!confirm("Quit Overlay Composer? Overlays in OBS will stop updating until it is started again.")) return;
  try { await api("/api/app/quit", "POST", {}); } catch { /* сервер уже закрылся */ }
  document.body.textContent = "Overlay Composer has been stopped. You can close this tab.";
});

interface ConfigView {
  host: string;
  port: number;
  debugProxy: boolean;
  obsEnabled: boolean;
  obsUrl: string;
  hasObsPassword: boolean;
  obs: ObsInfo;
  obsWsConnected: boolean;
  obsWsError: string | null;
  overlayUrl: string;
  restartRequired: boolean;
}

const settingsDialog = $<HTMLDialogElement>("settingsDialog");
let statusTimer = 0;

function showStatus(c: ConfigView) {
  const st = $("cfgStatus");
  st.className = `hint ${c.obs.connected ? "" : "warn"}`;
  st.textContent = c.obs.connected
    ? `OBS: connected via ${c.obs.via === "overlay" ? "overlay" : "obs-websocket"}` +
      (c.obs.scene ? ` · scene: ${c.obs.scene}` : "") +
      (c.obs.scenes.length ? ` · ${c.obs.scenes.length} scenes known` : "")
    : `OBS: no data — ${c.obs.error ?? "waiting…"}`;
  if (c.restartRequired) st.textContent += " · restart pending for host/port";

  const ws = $("cfgWsStatus");
  ws.className = `hint ${c.obsWsConnected ? "" : "warn"}`;
  ws.textContent = c.obsWsConnected
    ? "obs-websocket: connected"
    : `obs-websocket: not connected${c.obsWsError ? ` — ${c.obsWsError}` : " (connecting…)"}`;

  $("cfgOverlayUrl").textContent = c.overlayUrl;
  applyObsInfo(c.obs);
}

async function refreshStatus() {
  try { showStatus(await api<ConfigView>("/api/config")); } catch { /* сервер перезапускается */ }
  loadDevices();
  loadTwitch();
}

interface TwitchView {
  clientId: string;
  hasSecret: boolean;
  redirectUri: string;
  accounts: { role: string; login: string; scopes: string[]; error?: string }[];
}

const COPY_ICON = '<svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><rect x="9" y="9" width="11" height="11" rx="2"/><path d="M5 15V6a2 2 0 0 1 2-2h9"/></svg>';

async function copyText(text: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(text);
    return true;
  } catch { /* нет доступа к буферу (не защищённый контекст/запрет) — запасной способ */ }
  const ta = el("textarea", { value: text, style: "position:fixed;opacity:0;top:0;left:0" });
  document.body.append(ta);
  ta.select();
  let ok = false;
  try { ok = document.execCommand("copy"); } catch { ok = false; }
  ta.remove();
  return ok;
}

const TW_ROLES: [string, string][] = [["streamer", "Streamer"], ["bot", "Bot"]];
let twFormFilled = false;

// Раздел «Twitch accounts»; как и Remote devices, виден только на самом компьютере (с других сервер отвечает 403).
async function loadTwitch() {
  const box = $("twBox");
  let v: TwitchView;
  try { v = await api<TwitchView>("/api/twitch"); } catch { box.hidden = true; return; }
  box.hidden = false;
  $("twRedirect").textContent = v.redirectUri;
  if (!twFormFilled) { // поля заполняем один раз, чтобы опрос не затирал ввод
    $<HTMLInputElement>("twClientId").value = v.clientId;
    $<HTMLInputElement>("twClientSecret").placeholder = v.hasSecret ? "saved — leave empty to keep" : "client secret";
    twFormFilled = true;
  }
  $("twAppHint").textContent = v.clientId && v.hasSecret ? "" : "Enter Client ID and Client Secret, then connect accounts.";
  const ready = !!v.clientId && v.hasSecret;
  $("twAccounts").replaceChildren(...TW_ROLES.map(([role, title]) => {
    const acc = v.accounts.find((a) => a.role === role);
    const info = el("span", {
      class: acc?.error ? "hint warn" : "hint",
      textContent: acc ? `${title}: ${acc.login}${acc.error ? ` — ${acc.error}` : ""}` : `${title}: not connected`,
    });
    // Два аккаунта в одном браузере не подключить (Twitch помнит вход), поэтому вместо перехода — ссылка в буфер обмена:
    // её можно открыть в другом браузере/профиле/приватном окне, где выполнен вход нужным аккаунтом.
    const connect = el("button", {
      type: "button", class: "iconbtn", disabled: !ready,
      title: acc ? `Copy a link to reconnect the ${title.toLowerCase()} account` : `Copy the authorization link for the ${title.toLowerCase()} account`,
    });
    connect.setAttribute("aria-label", connect.title);
    connect.innerHTML = COPY_ICON; // статическая разметка без пользовательских данных
    connect.addEventListener("click", async () => {
      try {
        const r = await api<{ url: string }>(`/api/twitch/connect/${role}`, "POST", {});
        const ok = await copyText(r.url);
        toast(ok
          ? `${title} link copied. Open it in a browser where the ${title.toLowerCase()} Twitch account is signed in (a private window works). It is valid for 10 minutes.`
          : "Could not access the clipboard — copy the link from the prompt");
        if (!ok) prompt("Authorization link (Ctrl+C):", r.url);
      } catch (e) { fail(e); }
    });
    const btns = el("span", { class: "row" }, connect);
    if (acc) {
      const off = el("button", { type: "button", textContent: "Disconnect" });
      off.addEventListener("click", async () => {
        if (!confirm(`Disconnect Twitch account “${acc.login}”?`)) return;
        try { await api(`/api/twitch/accounts/${role}`, "DELETE"); loadTwitch(); } catch (e) { fail(e); }
      });
      btns.append(off);
    }
    return el("div", { class: "row", style: "justify-content:space-between" }, info, btns);
  }));
}

$("twSaveApp").addEventListener("click", async () => {
  try {
    await api("/api/twitch/app", "PUT", {
      clientId: $<HTMLInputElement>("twClientId").value,
      clientSecret: $<HTMLInputElement>("twClientSecret").value,
    });
    $<HTMLInputElement>("twClientSecret").value = "";
    twFormFilled = false;
    await loadTwitch();
    toast("Twitch application saved");
  } catch (e) { fail(e); }
});

interface DevicesView {
  code: string;
  expiresIn: number;
  devices: { id: string; name: string; createdAt: string; lastSeen: string; lastIp: string }[];
  remoteEnabled: boolean;
  pairUrl: string;
}

// Раздел «Remote devices» (внутри группы «Listen on»): одноразовый код существует только при Listen on = All network interfaces
// и по умолчанию скрыт — показывается по клику. Доступен только с этого компьютера (с других сервер отвечает 403).
let devView: DevicesView | null = null;
let codeShown = false;
let codeTimer: ReturnType<typeof setTimeout> | undefined;
const CODE_MASK = "••••••";

function paintCode() {
  const btn = $<HTMLButtonElement>("devCode");
  btn.textContent = codeShown && devView?.code ? devView.code : CODE_MASK;
  btn.classList.toggle("masked", !codeShown);
  btn.setAttribute("aria-pressed", String(codeShown));
}

function setCodeShown(show: boolean) {
  codeShown = show;
  clearTimeout(codeTimer);
  if (show) codeTimer = setTimeout(() => setCodeShown(false), 20000); // сам прячется — не остаётся на экране при демонстрации
  paintCode();
}
$("devCode").addEventListener("click", () => setCodeShown(!codeShown));

function paintDevHint() {
  const v = devView;
  if (!v) return;
  const wantsRemote = $<HTMLSelectElement>("cfgHost").value !== "127.0.0.1";
  $("devHint").textContent = v.remoteEnabled
    ? (wantsRemote ? "Devices that enter the code can open the editor and overlay from your network."
      : "You switched “Listen on” back to this computer only: pairing will stop after Save and restart. Already paired devices stay in the list until revoked.")
    : (wantsRemote ? "Pairing will be available after you Save and restart the application."
      : "Pairing is off while the server listens on this computer only. To connect another device (tablet, phone), set “Listen on” to “All network interfaces”, Save and restart.");
}

async function loadDevices() {
  const box = $("devBox");
  let v: DevicesView;
  try { v = await api<DevicesView>("/api/devices"); } catch { box.hidden = true; return; }
  devView = v;
  box.hidden = false;
  $("devPair").hidden = !v.remoteEnabled;
  if (!v.remoteEnabled && codeShown) setCodeShown(false);
  paintCode();
  paintDevHint();
  $("devLeft").textContent = v.remoteEnabled ? `changes in ${v.expiresIn}s` : "";
  $("devUrl").textContent = v.pairUrl.replace("127.0.0.1", location.hostname === "127.0.0.1" || location.hostname === "localhost" ? "<this-computer-ip>" : location.hostname);
  const list = $("devList");
  list.replaceChildren(...v.devices.map((d) => {
    const seen = new Date(d.lastSeen).toLocaleString();
    const rm = el("button", { type: "button", textContent: "Revoke" });
    rm.addEventListener("click", async () => {
      if (!confirm(`Revoke access for “${d.name}”?`)) return;
      try { await api(`/api/devices/${encodeURIComponent(d.id)}`, "DELETE"); loadDevices(); } catch (e) { fail(e); }
    });
    return el("div", { class: "row", style: "justify-content:space-between" },
      el("span", { class: "hint", textContent: `${d.name} · ${d.lastIp} · seen ${seen}` }), rm);
  }));
}

// Пока открыт диалог — обновляем статус, чтобы было видно результат Reconnect.
settingsDialog.addEventListener("close", () => { clearInterval(statusTimer); setCodeShown(false); });

$("cfgReconnectBtn").addEventListener("click", async () => {
  try {
    await api("/api/obs/reconnect", "POST", {});
    $("cfgWsStatus").textContent = "obs-websocket: connecting…";
    setTimeout(refreshStatus, 1500);
  } catch (e) { fail(e); }
});

// Клик по значку OBS в шапке открывает настройки с подробностями.
$("obsBadge").addEventListener("click", () => $("settingsBtn").click());

function updateHostWarn() {
  $("cfgHostWarn").hidden = $<HTMLSelectElement>("cfgHost").value === "127.0.0.1";
  paintDevHint();
}
$("cfgHost").addEventListener("change", updateHostWarn);
$("cfgObsEnabled").addEventListener("change", () => {
  $("cfgObsWsBox").hidden = !$<HTMLInputElement>("cfgObsEnabled").checked;
});

$("settingsBtn").addEventListener("click", async () => {
  try {
    const c = await api<ConfigView>("/api/config");
    $<HTMLInputElement>("cfgDebugProxy").checked = c.debugProxy;
    $<HTMLInputElement>("cfgObsEnabled").checked = c.obsEnabled;
    $("cfgObsWsBox").hidden = !c.obsEnabled;
    $<HTMLInputElement>("cfgObsUrl").value = c.obsUrl;
    const pw = $<HTMLInputElement>("cfgObsPass");
    pw.value = "";
    pw.placeholder = c.hasObsPassword ? "saved — leave empty to keep" : "no password";
    $<HTMLInputElement>("cfgObsClear").checked = false;
    $("cfgObsClearRow").hidden = !c.hasObsPassword;

    const hosts = [
      ["127.0.0.1", "This computer only (recommended)"],
      ["0.0.0.0", "All network interfaces"],
    ];
    if (!hosts.some(([v]) => v === c.host)) hosts.push([c.host, c.host]);
    const sel = $<HTMLSelectElement>("cfgHost");
    sel.replaceChildren(...hosts.map(([v, t]) => el("option", { value: v, textContent: t, selected: v === c.host })));
    updateHostWarn();
    $<HTMLInputElement>("cfgPort").value = String(c.port);
    showStatus(c);
    loadAppInfo();
    loadImportScenes();
    loadDevices();
    twFormFilled = false;
    loadTwitch();
    settingsDialog.showModal();
    clearInterval(statusTimer);
    statusTimer = setInterval(refreshStatus, 2000) as unknown as number;
  } catch (e) { fail(e); }
});

$("cfgSaveBtn").addEventListener("click", async () => {
  try {
    const c = await api<ConfigView>("/api/config", "PUT", {
      host: $<HTMLSelectElement>("cfgHost").value,
      port: Number($<HTMLInputElement>("cfgPort").value),
      debugProxy: $<HTMLInputElement>("cfgDebugProxy").checked,
      obs: {
        enabled: $<HTMLInputElement>("cfgObsEnabled").checked,
        url: $<HTMLInputElement>("cfgObsUrl").value.trim(),
        password: $<HTMLInputElement>("cfgObsPass").value,
        clearPassword: $<HTMLInputElement>("cfgObsClear").checked,
      },
    });
    settingsDialog.close();
    toast(c.restartRequired ? "Saved. Restart the server to apply host/port" : "Settings saved");
  } catch (e) { fail(e); }
});

// ---------- import scene from OBS ----------

interface ImportReport {
  layout: Layout;
  newSources: Source[];
  reusedSources: number;
  proxied: number;
  skipped: { name: string; reason: string }[];
  warnings: string[];
  bound: boolean;
}

async function loadImportScenes() {
  const sel = $<HTMLSelectElement>("impScene");
  const btn = $<HTMLButtonElement>("impBtn");
  try {
    const r = await api<{ scenes: string[] }>("/api/obs/scenes");
    sel.replaceChildren(...r.scenes.map((s) => el("option", { value: s, textContent: s })));
    sel.disabled = btn.disabled = r.scenes.length === 0;
    $("impHint").textContent = r.scenes.length
      ? "Browser Sources of the scene (including ones inside groups) become layers with the same size and position. External sites are added with proxy."
      : "No scenes yet — enable obs-websocket above.";
  } catch (e) {
    sel.disabled = btn.disabled = true;
    $("impHint").textContent = String(e);
  }
}

$("impBtn").addEventListener("click", async () => {
  const sceneName = $<HTMLSelectElement>("impScene").value;
  if (!sceneName) return;
  const btn = $<HTMLButtonElement>("impBtn");
  btn.disabled = true;
  try {
    const r = await api<ImportReport>("/api/obs/import", "POST", { scene: sceneName, bind: $<HTMLInputElement>("impBind").checked });
    await Promise.all([loadSources(), loadLayouts()]);
    const lines = [
      `Imported “${r.layout.name}”: ${r.layout.objects.length} layer(s), ${r.newSources.length} new source(s), ${r.proxied} via proxy` +
        (r.bound ? ", bound to the scene" : ""),
      ...r.skipped.map((x) => `skipped ${x.name}: ${x.reason}`),
      ...r.warnings.map((x) => `warning: ${x}`),
    ];
    $("impResult").textContent = lines.join("\n");
    settingsDialog.close();
    await openLayout(r.layout.id, true);
    toast(lines[0]);
  } catch (e) { fail(e); } finally { btn.disabled = false; }
});

// ---------- save ----------

async function save() {
  if (!layout) return;
  try {
    await api(`/api/layouts/${encodeURIComponent(layout.id)}`, "PUT", layout);
    dirty = false;
    await loadLayouts();
    renderLayouts();
    updateBadges();
    toast("Saved");
  } catch (e) { fail(e); }
}

$("saveBtn").addEventListener("click", save);

// ---------- keyboard ----------

addEventListener("keydown", (e) => {
  if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === "s") {
    e.preventDefault();
    save();
    return;
  }
  const tag = (e.target as HTMLElement).tagName;
  if (tag === "INPUT" || tag === "SELECT" || tag === "TEXTAREA") return;
  if ((e.ctrlKey || e.metaKey) && !e.altKey && e.key.toLowerCase() === "c" && selected()) {
    e.preventDefault();
    copySelected().catch(fail);
    return;
  }
  if ((e.ctrlKey || e.metaKey) && !e.altKey && e.key.toLowerCase() === "v" && clip) {
    e.preventDefault();
    pasteClip();
    return;
  }
  const o = selected();
  if (!o) return;
  if (e.key === "Escape") return select(null);
  if (e.key === "Delete" || e.key === "Backspace") {
    e.preventDefault();
    return removeObject(o.id);
  }
  const step = e.shiftKey ? 10 : 1;
  const moves: Record<string, [number, number]> = {
    ArrowLeft: [-step, 0], ArrowRight: [step, 0], ArrowUp: [0, -step], ArrowDown: [0, step],
  };
  const m = moves[e.key];
  if (m && !o.locked) {
    e.preventDefault();
    o.transform.x += m[0];
    o.transform.y += m[1];
    touch(o);
  }
});

addEventListener("beforeunload", (e) => {
  if (dirty) e.preventDefault();
});

addEventListener("resize", () => {
  fitView();
  renderOverlay();
});

// ---------- websocket ----------

let ws: WebSocket | null = null;

function wsSend(msg: unknown) {
  if (ws?.readyState === WebSocket.OPEN) ws.send(JSON.stringify(msg));
}

function setObsStatus(obs: ObsInfo) {
  const b = $("obsBadge");
  b.textContent = obs.connected ? `OBS: connected (${obs.via === "overlay" ? "overlay" : "websocket"})` : "OBS: no data ⓘ";
  b.className = `badge ${obs.connected ? "ok" : "bad"}`;
  b.title = obs.connected
    ? `Scene data comes from ${obs.via === "overlay" ? "the overlay running in OBS" : "obs-websocket"}`
    : `${obs.error ?? "Not connected"}\n\nClick for details`;
  $("sceneBadge").textContent = obs.scene ? `scene: ${obs.scene}` : "";
}

/** Статус + список сцен для подсказок в поле «OBS scene» (без пересоздания панели свойств). */
function applyObsInfo(obs: ObsInfo) {
  setObsStatus(obs);
  if (JSON.stringify(obsScenes) !== JSON.stringify(obs.scenes)) {
    obsScenes = obs.scenes;
    renderSceneBox();
  }
}

function connectWs() {
  ws = new WebSocket(`${location.protocol === "https:" ? "wss" : "ws"}://${location.host}/ws`);
  ws.onopen = () => pluginHost.onConnected();
  ws.onmessage = async (ev) => {
    const msg = JSON.parse(ev.data) as ServerMessage;
    if (pluginHost.onServerMessage(msg as unknown as { type: string })) return;
    try {
      switch (msg.type) {
        case "hello":
          activeLayoutId = msg.layoutId;
          applyObsInfo(msg.obs);
          renderLayouts();
          break;
        case "obs:status":
          applyObsInfo(msg.obs);
          break;
        case "scene:changed":
          if (msg.layoutId) activeLayoutId = msg.layoutId;
          if (msg.scene) $("sceneBadge").textContent = `scene: ${msg.scene}`;
          renderLayouts();
          break;
        case "layouts:changed":
          await loadLayouts();
          renderLayouts();
          break;
        case "sources:changed":
          await loadSources();
          renderLayers();
          renderProps();
          break;
        case "layout:updated":
          // Чужое изменение подтягиваем, только если у нас нет несохранённых правок.
          if (layout && msg.layoutId === layout.id && !dirty) {
            layout = await api<Layout>(`/api/layouts/${encodeURIComponent(layout.id)}`);
            if (selectedId && !selected()) selectedId = null;
            refreshAll();
          }
          break;
      }
    } catch (e) { console.warn(e); }
  };
  ws.onclose = () => setTimeout(connectWs, 1500);
}

init().catch(fail);
