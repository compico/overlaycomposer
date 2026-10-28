import { PluginHost } from "../shared/pluginhost.ts";
import type { Layout, LayoutObject, ObsInfo, PluginManifest, PluginSetting, ServerMessage, Source, Transform } from "../../shared/types.ts";
import { contentCenter, defaultTransform, placeByContentCenter, uid, visibleBox } from "../../shared/types.ts";
import { OverlayScene } from "../shared/scene.ts";
import { mountLayers } from "./features/layers/view.ts";
import { mountLayouts } from "./features/layouts/view.ts";
import { mountKeyboard } from "./features/keyboard/view.ts";
import { createLiveHandler } from "./features/live/model.ts";
import { connectLive, type LiveConnection } from "./features/live/view.ts";
import { createCopyModel } from "./features/copy/model.ts";
import { mountCopy } from "./features/copy/view.ts";
import { createAddSourceModel, maskUrl, MIN_SIZE } from "./features/add-source/model.ts";
import { mountAddSource } from "./features/add-source/view.ts";
import { createEditorModel } from "./features/editor/model.ts";
import { $, el } from "./lib/dom.ts";
import { type Api, httpApi } from "./lib/api.ts";
import { createNotificationsModel } from "./features/notifications/model.ts";
import { mountNotifications } from "./features/notifications/view.ts";
import { createStoreModel } from "./features/store/model.ts";
import { mountStore } from "./features/store/view.ts";
import { PERM_TEXT } from "./lib/permissions.ts";
import { copyText, COPY_ICON } from "./lib/clipboard.ts";
import { createAppModel } from "./features/app/model.ts";
import { mountApp } from "./features/app/view.ts";
import { createConfigModel } from "./features/config/model.ts";
import { mountConfig } from "./features/config/view.ts";
import { createTwitchModel } from "./features/twitch/model.ts";
import { mountTwitch } from "./features/twitch/view.ts";
import { createDevicesModel } from "./features/devices/model.ts";
import { mountDevices } from "./features/devices/view.ts";
import { createObsImportModel } from "./features/obs-import/model.ts";
import { mountObsImport } from "./features/obs-import/view.ts";
import { mountSceneBinding } from "./features/scene-binding/view.ts";
import { boundsOf, snapMove, snapResize, type Guide, type Rect } from "../../shared/snap.ts";



// ---------- state ----------

const api: Api = httpApi;

// Состояние живёт в модели редактора (features/editor). Эти переменные — только для чтения: «зеркало» модели,
// обновляется синхронно при каждом её изменении. Менять состояние — только через `editor.*`.
const editor = createEditorModel({ api, confirmDiscard: () => confirm("Discard unsaved changes?") });
let layouts: Layout[] = [];
let sources: Source[] = [];
let plugins: PluginManifest[] = [];
let layout: Layout | null = null; // рабочая копия, сохраняется кнопкой Save
let selectedId: string | null = null;
let dirty = false;
let obsScenes: string[] = [];
/** layout для сцен без собственного layout */
let defaultLayoutId: string | null = null;
let activeLayoutId: string | null = null;
editor.subscribe((st) => {
  ({ layouts, sources, plugins, layout, selectedId, dirty, obsScenes, defaultLayoutId, activeLayoutId } = st);
});
let k = 1; // экранных px на 1 px canvas
let propSync: (() => void)[] = [];
/** Буфер Ctrl+C: снимок слоя и layout, из которого он скопирован (живёт, пока открыта страница). */

// Предпросмотр плагинов в редакторе: чат читается, но писать (chat.say) нельзя — ответы шлёт только оверлей.
const pluginHost = new PluginHost({ send: (m) => wsSend(m), canSend: false });
const scene = new OverlayScene($("stage"), { interactive: true, plugins: pluginHost });

// ---------- helpers ----------

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

const sourceOf = (id: string) => sources.find((s) => s.id === id);
const selected = () => layout?.objects.find((o) => o.id === selectedId) ?? null;
const objName = (o: LayoutObject) => o.name || (sourceOf(o.sourceId)?.name ?? `(missing ${o.sourceId})`);

const markDirty = () => editor.markDirty();

// ---------- layout mode and snapping (настройки помним между запусками) ----------

const pref = {
  get(key: string, def: boolean): boolean {
    try { const v = localStorage.getItem("oc." + key); return v === null ? def : v === "1"; } catch { return def; }
  },
  set(key: string, v: boolean) { try { localStorage.setItem("oc." + key, v ? "1" : "0"); } catch { /* хранилище недоступно */ } },
};
let composeMode = pref.get("compose", false);
let snapOn = pref.get("snap", true);
const SNAP_PX = 8; // порог прилипания в экранных пикселях

function syncToggles() {
  $("canvasFrame").classList.toggle("compose", composeMode);
  $("composeBtn").setAttribute("aria-pressed", String(composeMode));
  $("snapBtn").setAttribute("aria-pressed", String(snapOn));
}
function toggleCompose() { composeMode = !composeMode; pref.set("compose", composeMode); syncToggles(); renderOverlay(); }
function toggleSnap() { snapOn = !snapOn; pref.set("snap", snapOn); syncToggles(); toast(snapOn ? "Snapping on" : "Snapping off"); }
$("composeBtn").addEventListener("click", toggleCompose);
$("snapBtn").addEventListener("click", toggleSnap);
syncToggles();

/** Рамки других видимых виджетов — цели прилипания. */
function snapTargets(except: LayoutObject): Rect[] {
  return layout ? layout.objects.filter((o) => o.visible && o !== except).map((o) => boundsOf(o.transform)) : [];
}

function drawGuides(guides: Guide[]) {
  const layer = $("guideLayer");
  layer.replaceChildren(...guides.map((g) => {
    const d = el("div", { class: "guide" });
    if (g.axis === "x") { d.style.left = `${g.pos * k}px`; d.style.top = `${g.from * k}px`; d.style.width = "1px"; d.style.height = `${(g.to - g.from) * k}px`; }
    else { d.style.top = `${g.pos * k}px`; d.style.left = `${g.from * k}px`; d.style.height = "1px"; d.style.width = `${(g.to - g.from) * k}px`; }
    return d;
  }));
}

const snapActive = (e: PointerEvent) => snapOn && !e.ctrlKey && !e.metaKey; // Ctrl на время жеста — без прилипания

// ---------- history of edits (Ctrl+Z / Ctrl+Y): логика — в editor model ----------

function undo() { if (!editor.undo()) toast("Nothing to undo"); }
function redo() { if (!editor.redo()) toast("Nothing to redo"); }
$("undoBtn").addEventListener("click", undo);
$("redoBtn").addEventListener("click", redo);

function updateBadges() {
  $("dirtyBadge").textContent = dirty ? "● unsaved" : "";
  $("canvasSize").textContent = layout ? `${layout.canvas.width}×${layout.canvas.height}` : "";
}

// ---------- loading (логика — в editor model; здесь только перерисовка по ChangeKind) ----------

const loadSources = () => editor.loadSources();
const loadPlugins = () => editor.loadPlugins();
const loadLayouts = () => editor.loadLayouts();
async function openLayout(id: string, force = false) { await editor.openLayout(id, force); }

async function init() {
  applyObsInfo(await editor.bootstrap());
  connectWs();
}

function refreshAll() {
  drawLayouts();
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
    if (composeMode) box.append(el("span", { class: "tag" }, objName(o)));

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
      track(ev, (dx, dy, e) => {
        let nx = Math.round(x0 + dx), ny = Math.round(y0 + dy);
        let guides: Guide[] = [];
        if (snapActive(e) && layout) {
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
  editor.touch();
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
    drawGuides([]);
    editor.flush(); // жест мыши — один шаг истории
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
    // Прилипание краёв (только без поворота и без Shift — пропорции важнее): тянем только двигаемые края.
    if (snapActive(e) && layout && !e.shiftKey && !t.rotationZ) {
      const left = cx0 - w0 / 2, top = cy0 - h0 / 2;
      const r: Rect = {
        x: dirx < 0 ? left + w0 - w : left, y: diry < 0 ? top + h0 - h : top, w, h,
      };
      const sn = snapResize(r, { l: dirx < 0, r: dirx > 0, t: diry < 0, b: diry > 0 }, snapTargets(o), { threshold: SNAP_PX / k, canvas: layout.canvas });
      w = Math.max(MIN_SIZE, sn.rect.w);
      h = Math.max(MIN_SIZE, sn.rect.h);
      drawGuides(sn.guides);
    } else drawGuides([]);
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

const select = (id: string | null) => editor.select(id);

$("viewport").addEventListener("pointerdown", (e) => {
  if (!(e.target as HTMLElement).closest(".box")) select(null);
});

// ---------- layouts panel (вид — features/layouts) ----------

const renderLayouts = mountLayouts(
  { list: $("layoutList"), select: $<HTMLSelectElement>("layoutSelect"), deleteBtn: $("delLayoutBtn") },
  { open: (id) => openLayout(id).catch(fail), activate: (id) => wsSend({ type: "layout:activate", layoutId: id }) },
);
const drawLayouts = () => renderLayouts(editor.get());

$("newLayoutBtn").addEventListener("click", async () => {
  const name = prompt("Layout name (will also be used as OBS scene name):", "New Layout");
  if (!name) return;
  try { await editor.createLayout(name); } catch (e) { fail(e); }
});

$("dupLayoutBtn").addEventListener("click", async () => {
  if (!layout) return;
  const name = prompt("Name for the copy:", `${layout.name} copy`);
  if (!name) return;
  try { await editor.duplicateCurrent(name); } catch (e) { fail(e); }
});

$("delLayoutBtn").addEventListener("click", async () => {
  if (!layout || layouts.length <= 1) return;
  if (!confirm(`Delete layout "${layout.name}"?`)) return;
  try { await editor.deleteCurrent(); } catch (e) { fail(e); }
});

// ---------- layers panel (вид — features/layers) ----------

const renderLayersPanel = mountLayers($("layerList"), {
  select, setVisible: editor.setVisible, setLocked: editor.setLocked, rename: editor.rename,
  removeObject: editor.removeObject, reorder: editor.reorder, askName: (cur) => prompt("Layer name:", cur),
});
const renderLayers = () => renderLayersPanel(editor.get());
const removeObject = (id: string) => editor.removeObject(id);

// ---------- copy between layouts (логика — features/copy) ----------

const copy = createCopyModel({ api, editor: { ...editor, loadLayouts: loadLayouts }, toast });
mountCopy(
  { dialog: $<HTMLDialogElement>("copyDialog"), what: $("copyWhat"), target: $<HTMLSelectElement>("copyTarget"), linked: $<HTMLInputElement>("copyLinked"), openBtn: $("copyObjBtn"), goBtn: $("copyGoBtn") },
  copy, { toast, fail },
);
const copySelected = () => copy.copySelected();
const pasteClip = () => copy.paste();

// ---------- add source (логика — features/add-source) ----------

const addSourceModel = createAddSourceModel({
  api,
  editor,
  confirmPermissions: (p, list) => confirm(
    `“${p.name}” asks for permissions:\n\n${list.map((x) => `• ${PERM_TEXT[x] ?? x}`).join("\n")}\n\nAllow? (You can change this later in the widget properties.)`),
});
const addSource = mountAddSource(
  {
    dialog: $<HTMLDialogElement>("addDialog"),
    tabs: [...document.querySelectorAll<HTMLButtonElement>("#addDialog .tab")],
    panes: { iframe: $("pane-iframe"), existing: $("pane-existing"), plugin: $("pane-plugin") },
    tabExisting: $("tabExisting"),
    newName: $<HTMLInputElement>("newName"), newUrl: $<HTMLInputElement>("newUrl"), newProxy: $<HTMLInputElement>("newProxy"),
    newW: $<HTMLInputElement>("newW"), newH: $<HTMLInputElement>("newH"), createBtn: $("createBtn"),
    existingList: $("existingList"), useExistingBtn: $("useExistingBtn"),
    pluginList: $("pluginList"), pluginHint: $("pluginHint"), usePluginBtn: $("usePluginBtn"),
    openBtn: $("addObjBtn"),
  },
  addSourceModel,
  { sources: () => sources, plugins: () => plugins, refreshPlugins: () => loadPlugins().catch(fail), toast, fail },
);
const renderPluginPicker = addSource.renderPlugins;

// ---------- plugin store ----------

/** Плагин поменялся на диске: подхватить в редакторе (список для Add Source, виджеты на холсте). */
async function afterPluginChange() {
  await loadPlugins().catch(fail);
  renderPluginPicker();
  await loadSources().catch(fail);
  refreshCanvas();
  renderLayers();
  renderProps();
}

const pluginStore = mountStore(
  {
    openBtn: $("openStoreBtn"), dialog: $<HTMLDialogElement>("storeDialog"), body: $("storeBody"), refreshBtn: $("storeRefreshBtn"),
    repoInput: $<HTMLInputElement>("storeRepo"), addBtn: $<HTMLButtonElement>("storeAddBtn"), tokenSummary: $("storeTokenSummary"),
    tokenInput: $<HTMLInputElement>("storeToken"), tokenSave: $("storeTokenSave"), tokenClear: $<HTMLButtonElement>("storeTokenClear"),
    openPluginsBtn: $("openPluginsBtn"),
  },
  createStoreModel({ api, afterChange: afterPluginChange }),
  {
    ui: { toast, fail },
    usedBy: (id) => sources.filter((x) => x.type === "plugin" && x.data.plugin === id).length,
    pluginName: (id) => plugins.find((x) => x.id === id)?.name,
    openPluginsFolder: () => api("/api/app/open", "POST", { what: "plugins" }),
  },
);

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
        await loadSources();
        editor.dropSource(src.id);
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
      drawLayouts();
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

const sceneBinding = mountSceneBinding(() => document.getElementById("sceneBox"), {
  current: () => layout,
  layouts: () => layouts,
  obsScenes: () => obsScenes,
  changed: markDirty,
});
const renderSceneBox = sceneBinding.render;

// ---------- settings (config.json): приложение, сервер/OBS, Twitch, устройства ----------

const appModel = createAppModel(api);
mountApp(
  {
    version: $("cfgVersion"), paths: $("cfgPaths"), autostart: $<HTMLInputElement>("cfgAutostart"), autostartHint: $("cfgAutostartHint"),
    trayHint: $("cfgTrayHint"), openData: $("cfgOpenData"), openLogs: $("cfgOpenLogs"), quit: $("cfgQuit"),
  },
  appModel,
  { toast, fail },
);

const twitchModel = createTwitchModel(api);
const twitchView = mountTwitch(
  {
    box: $("twBox"), redirect: $("twRedirect"), clientId: $<HTMLInputElement>("twClientId"), clientSecret: $<HTMLInputElement>("twClientSecret"),
    appHint: $("twAppHint"), accounts: $("twAccounts"), saveApp: $("twSaveApp"),
  },
  twitchModel,
  { toast, fail },
);

const devicesModel = createDevicesModel(api);
const devicesView = mountDevices(
  {
    box: $("devBox"), pair: $("devPair"), code: $<HTMLButtonElement>("devCode"), left: $("devLeft"), url: $("devUrl"),
    hint: $("devHint"), list: $("devList"),
  },
  devicesModel,
  { ui: { toast, fail }, wantsRemote: () => $<HTMLSelectElement>("cfgHost").value !== "127.0.0.1" },
);

const settings = mountConfig(
  {
    openBtn: $("settingsBtn"), obsBadge: $("obsBadge"), dialog: $<HTMLDialogElement>("settingsDialog"), status: $("cfgStatus"),
    wsStatus: $("cfgWsStatus"), overlayUrl: $("cfgOverlayUrl"), debugProxy: $<HTMLInputElement>("cfgDebugProxy"),
    obsEnabled: $<HTMLInputElement>("cfgObsEnabled"), obsWsBox: $("cfgObsWsBox"), obsUrl: $<HTMLInputElement>("cfgObsUrl"),
    obsPass: $<HTMLInputElement>("cfgObsPass"), obsClear: $<HTMLInputElement>("cfgObsClear"), obsClearRow: $("cfgObsClearRow"),
    host: $<HTMLSelectElement>("cfgHost"), hostWarn: $("cfgHostWarn"), port: $<HTMLInputElement>("cfgPort"),
    saveBtn: $("cfgSaveBtn"), reconnectBtn: $("cfgReconnectBtn"),
  },
  createConfigModel(api),
  {
    ui: { toast, fail },
    onOpen() {
      void appModel.load();
      loadImportScenes();
      void devicesModel.load();
      twitchView.resetForm();
      void twitchModel.load();
    },
    onTick() { void devicesModel.load(); void twitchModel.load(); },
    onClose() { devicesView.hideCode(); },
    onHostChange() { devicesView.repaintHint(); },
    onObsInfo: applyObsInfo,
  },
);

// ---------- import scene from OBS ----------

const obsImport = mountObsImport(
  { scene: $<HTMLSelectElement>("impScene"), btn: $<HTMLButtonElement>("impBtn"), hint: $("impHint"), bind: $<HTMLInputElement>("impBind"), result: $("impResult") },
  createObsImportModel(api),
  {
    ui: { toast, fail },
    async afterImport(r) {
      await Promise.all([loadSources(), loadLayouts()]);
      settings.closeDialog();
      await openLayout(r.layout.id, true);
    },
  },
);
const loadImportScenes = obsImport.load;

// ---------- save ----------

async function save() {
  if (!layout) return;
  try {
    await editor.save();
    toast("Saved");
  } catch (e) { fail(e); }
}

$("saveBtn").addEventListener("click", save);

// ---------- keyboard (сопоставление клавиш — features/keyboard; здесь — исполнение команд) ----------

mountKeyboard({
  context: () => ({ hasSelection: !!selected(), selectionLocked: !!selected()?.locked, hasClip: copy.hasClip() }),
  run(cmd) {
    const o = selected();
    switch (cmd.type) {
      case "save": void save(); break;
      case "undo": undo(); break;
      case "redo": redo(); break;
      case "copy": copySelected().catch(fail); break;
      case "paste": pasteClip(); break;
      case "toggleCompose": toggleCompose(); break;
      case "deselect": select(null); break;
      case "delete": if (o) removeObject(o.id); break;
      case "nudge":
        if (o) { o.transform.x += cmd.dx; o.transform.y += cmd.dy; touch(o); }
        break;
    }
  },
});

addEventListener("beforeunload", (e) => {
  if (dirty) e.preventDefault();
});

addEventListener("resize", () => {
  fitView();
  renderOverlay();
});

// ---------- websocket ----------

let live: LiveConnection | null = null;

function wsSend(msg: unknown) { live?.send(msg); }

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
  editor.setObsScenes(obs.scenes); // при изменении модель сообщит «meta» → перерисуется блок привязки сцен
}

function connectWs() {
  const handle = createLiveHandler({
    setActive: (id) => editor.setActive(id),
    obs: applyObsInfo,
    scene: (name) => { $("sceneBadge").textContent = `scene: ${name}`; },
    loadLayouts, loadSources,
    reloadCurrent: () => editor.reloadCurrent(),
    current: () => (layout ? { id: layout.id, dirty } : null),
  });
  live = connectLive({
    intercept: (m) => pluginHost.onServerMessage(m as unknown as { type: string }),
    handle,
    onOpen: () => pluginHost.onConnected(),
  });
}

// ---------- представление: перерисовка по виду изменения модели ----------

editor.subscribe((st) => {
  $("undoBtn").toggleAttribute("disabled", !st.canUndo);
  $("redoBtn").toggleAttribute("disabled", !st.canRedo);
  switch (st.change) {
    case "reset": refreshAll(); break;
    case "structure":
      refreshCanvas(); renderLayers(); renderProps(); updateBadges(); break;
    case "layers":
      renderOverlay(); renderLayers(); updateBadges(); break;
    case "selection":
      renderOverlay(); renderLayers(); renderProps(); break;
    case "geometry": updateBadges(); break;
    case "meta": drawLayouts(); renderSceneBox(); updateBadges(); break;
    case "data": renderLayers(); renderProps(); break;
  }
});

init().catch(fail);

// Проверка обновлений на сервере идёт чуть позже старта — один раз перепроверяем, чтобы подсказка появилась без захода в настройки.
setTimeout(() => void appModel.load(), 15000);

// ---------- notification center ----------

mountNotifications(
  { bell: $("bellBtn"), count: $("bellCount"), panel: $("notifPanel"), dialog: $<HTMLDialogElement>("notifDialog"), detail: $("notifDetail"), actions: $("notifActions") },
  createNotificationsModel(api),
  { ui: { toast, fail }, openStore: pluginStore.open },
);
