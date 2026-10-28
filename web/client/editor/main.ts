import { PluginHost } from "../shared/pluginhost.ts";
import type { Layout, LayoutObject, ObsInfo, PluginManifest, PluginSetting, ServerMessage, Source, Transform } from "../../shared/types.ts";
import { visibleBox } from "../../shared/types.ts";
import { OverlayScene } from "../shared/scene.ts";
import { mountLayers } from "./features/layers/view.ts";
import { mountLayouts } from "./features/layouts/view.ts";
import { mountKeyboard } from "./features/keyboard/view.ts";
import { createLiveHandler } from "./features/live/model.ts";
import { connectLive, type LiveConnection } from "./features/live/view.ts";
import { createCopyModel } from "./features/copy/model.ts";
import { mountCopy } from "./features/copy/view.ts";
import { createAddSourceModel } from "./features/add-source/model.ts";
import { mountAddSource } from "./features/add-source/view.ts";
import { createPluginSettingsModel } from "./features/plugin-settings/model.ts";
import { mountPluginSettings } from "./features/plugin-settings/view.ts";
import { createPropertiesModel } from "./features/properties/model.ts";
import { mountProperties } from "./features/properties/view.ts";
import { createViewPrefsModel } from "./features/view-prefs/model.ts";
import { mountViewPrefs } from "./features/view-prefs/view.ts";
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
import { beginCrop, beginResize, boxRect, fitScale, objectsAtPoint, rotationAngle } from "../../shared/gestures.ts";



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

const prefsStorage = {
  get: (k: string) => localStorage.getItem(k),
  set: (k: string, v: string) => localStorage.setItem(k, v),
};
const viewPrefs = createViewPrefsModel(prefsStorage);
const SNAP_PX = 8; // порог прилипания в экранных пикселях
const toggleCompose = () => viewPrefs.toggleCompose();
// вызывается после объявления renderOverlay и toast (function declarations поднимаются)
mountViewPrefs(
  { frame: $("canvasFrame"), composeBtn: $("composeBtn"), snapBtn: $("snapBtn") },
  viewPrefs,
  { composeChanged: () => renderOverlay(), toast },
);

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

const snapActive = (e: PointerEvent) => viewPrefs.get().snap && !e.ctrlKey && !e.metaKey; // Ctrl на время жеста — без прилипания

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
  k = fitScale(vp.clientWidth, vp.clientHeight, W, H);
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
    if (viewPrefs.get().compose) box.append(el("span", { class: "tag" }, objName(o)));

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
  return objectsAtPoint(layout.objects, (ev.clientX - r.left) / k, (ev.clientY - r.top) / k);
}

function positionBox(box: HTMLElement, o: LayoutObject) {
  const r = boxRect(o.transform, k); // рамка — видимая (необрезанная) часть
  box.style.left = `${r.left}px`;
  box.style.top = `${r.top}px`;
  box.style.width = `${r.width}px`;
  box.style.height = `${r.height}px`;
  box.style.transform = `rotate(${r.rotation}deg)`;
}

/** Применить изменение объекта во время drag: сцена + рамка + поля свойств. */
function touch(o: LayoutObject) {
  editor.touch();
  scene.setLayout(layout!);
  const box = $("overlayLayer").querySelector<HTMLElement>(`[data-id="${o.id}"]`);
  if (box) positionBox(box, o);
  props.sync();
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
  const apply = beginResize(o.transform, dirx, diry);
  track(ev, (dx, dy, e) => {
    const guides = apply(dx, dy, {
      keepAspect: e.shiftKey,
      snap: snapActive(e) && layout
        ? (r, edges) => snapResize(r, edges, snapTargets(o), { threshold: SNAP_PX / k, canvas: layout!.canvas })
        : undefined,
    });
    drawGuides(guides);
    touch(o);
  });
}

/** Alt+перетаскивание ручки — обрезка (как в OBS); математика — shared/gestures. */
function startCrop(ev: PointerEvent, o: LayoutObject, dirx: number, diry: number) {
  const apply = beginCrop(o.transform, dirx, diry);
  track(ev, (dx, dy) => {
    apply(dx, dy);
    touch(o);
  });
}

function startRotate(ev: PointerEvent, o: LayoutObject) {
  const t = o.transform;
  const { cx, cy } = visibleBox(t); // вращение вокруг центра видимой части (как в OBS)
  const rect = $("canvasFrame").getBoundingClientRect();
  track(ev, (_dx, _dy, e) => {
    t.rotationZ = rotationAngle(cx, cy, (e.clientX - rect.left) / k, (e.clientY - rect.top) / k, e.shiftKey);
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

// ---------- properties panel (features/properties) и форма настроек плагина (features/plugin-settings) ----------

const pluginForm = mountPluginSettings(createPluginSettingsModel(api), {
  fail,
  rollback: () => renderProps(),
  confirm: (t) => confirm(t),
});

const props = mountProperties($("props"), createPropertiesModel(api), {
  state: () => editor.get(),
  fieldEdited() {
    markDirty();
    scene.setLayout(layout!);
    renderOverlay();
  },
  touched: (o) => touch(o),
  layoutRenamed(name) { layout!.name = name; markDirty(); },
  canvasChanged() { markDirty(); refreshCanvas(); },
  removeObject: (id) => removeObject(id),
  async sourceDeleted(id) {
    // сервер вычистил объекты в сохранённых layout; убираем и из рабочей копии
    await loadSources();
    editor.dropSource(id);
  },
  async defaultChanged() {
    await loadLayouts();
    drawLayouts();
  },
  pluginForm,
  renderSceneBox: () => renderSceneBox(),
  toast,
  fail,
  confirm: (t) => confirm(t),
});
const renderProps = props.render;

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
