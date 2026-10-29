import { PluginHost } from "../shared/pluginhost.ts";
import type { Layout, LayoutObject, ObsInfo, PluginManifest, PluginSetting, ServerMessage, Source, Transform } from "../../shared/types.ts";
import { OverlayScene } from "../shared/scene.ts";
import { mountLayers } from "./features/layers/view.ts";
import { mountLayouts } from "./features/layouts/view.ts";
import { mountKeyboard } from "./features/keyboard/view.ts";
import { createLiveHandler } from "./features/live/model.ts";
import { connectLive, type LiveConnection } from "./features/live/view.ts";
import { createCopyModel } from "./features/copy/model.ts";
import { mountCopy } from "./features/copy/view.ts";
import { mountCanvas } from "./features/canvas/view.ts";
import { createAddSourceModel } from "./features/add-source/model.ts";
import { mountAddSource } from "./features/add-source/view.ts";
import { createPluginSettingsModel } from "./features/plugin-settings/model.ts";
import { mountPluginSettings } from "./features/plugin-settings/view.ts";
import { createPropertiesModel } from "./features/properties/model.ts";
import { mountProperties } from "./features/properties/view.ts";
import { createViewPrefsModel } from "./features/view-prefs/model.ts";
import { mountViewPrefs } from "./features/view-prefs/view.ts";
import { createToaster } from "./lib/toast.ts";
import { mountObsStatus } from "./features/obs-status/view.ts";
import { mountHistory } from "./features/history/view.ts";
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
import { mountTwitch, mountTwitchBadge } from "./features/twitch/view.ts";
import { createDevicesModel } from "./features/devices/model.ts";
import { mountDevices } from "./features/devices/view.ts";
import { createObsImportModel } from "./features/obs-import/model.ts";
import { mountObsImport } from "./features/obs-import/view.ts";
import { mountSceneBinding } from "./features/scene-binding/view.ts";



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

// Предпросмотр плагинов в редакторе: чат читается, но писать (chat.say) нельзя — ответы шлёт только оверлей.
const pluginHost = new PluginHost({ send: (m) => wsSend(m), canSend: false });
const scene = new OverlayScene($("stage"), { interactive: true, plugins: pluginHost });

// ---------- helpers ----------

const { toast, fail } = createToaster($("toast"));

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
const toggleCompose = () => viewPrefs.toggleCompose();
// вызывается после объявления renderOverlay и toast (function declarations поднимаются)
mountViewPrefs(
  { frame: $("canvasFrame"), composeBtn: $("composeBtn"), snapBtn: $("snapBtn") },
  viewPrefs,
  { composeChanged: () => renderOverlay(), toast },
);

// ---------- history of edits (Ctrl+Z / Ctrl+Y): логика — в editor model ----------

function undo() { if (!editor.undo()) toast("Nothing to undo"); }
function redo() { if (!editor.redo()) toast("Nothing to redo"); }
mountHistory({ btn: $("historyBtn"), panel: $("historyPanel") }, editor, { jumpTo: (i) => editor.jumpTo(i) });
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

// ---------- канвас: рамки, ручки, жесты (features/canvas) ----------

const canvas = mountCanvas(
  { frame: $("canvasFrame"), viewport: $("viewport"), overlay: $("overlayLayer"), guides: $("guideLayer") },
  {
    state: () => editor.get(),
    name: (o) => objName(o),
    compose: () => viewPrefs.get().compose,
    snap: (e) => viewPrefs.get().snap && !e.ctrlKey && !e.metaKey, // Ctrl на время жеста — без прилипания
    select: (id) => editor.select(id),
    edited(_o, label) {
      editor.touch(label);
      scene.setLayout(layout!);
      props.sync();
    },
    gestureEnd: () => editor.flush(), // жест мыши — один шаг истории
    setLayout: (l) => scene.setLayout(l),
    fit: (scale) => scene.fit({ scale, offsetX: 0, offsetY: 0 }),
  },
);
const refreshCanvas = canvas.refresh;
const renderOverlay = canvas.render;
const touch = canvas.touch;

// ---------- selection ----------

const select = (id: string | null) => editor.select(id);

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
  fieldEdited(label) {
    editor.markDirty("geometry", label);
    scene.setLayout(layout!);
    renderOverlay();
  },
  touched: (o, label) => touch(o, label),
  layoutRenamed(name) { layout!.name = name; editor.markDirty("geometry", "Rename layout"); },
  canvasChanged() { editor.markDirty("geometry", "Resize canvas"); refreshCanvas(); },
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

mountTwitchBadge($("twitchBadge"), twitchModel, () => $("settingsBtn").click());
// Проверяем при старте и раз в 5 минут: токен может «протухнуть» уже во время работы.
void twitchModel.load();
setInterval(() => void twitchModel.load(), 5 * 60_000);

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
        if (o) { o.transform.x += cmd.dx; o.transform.y += cmd.dy; touch(o, "Nudge"); }
        break;
    }
  },
});

addEventListener("beforeunload", (e) => {
  if (dirty) e.preventDefault();
});

// ---------- websocket ----------

let live: LiveConnection | null = null;

function wsSend(msg: unknown) { live?.send(msg); }

const obsStatus = mountObsStatus({ badge: $("obsBadge"), scene: $("sceneBadge") });

/** Статус + список сцен для подсказок в поле «OBS scene» (без пересоздания панели свойств). */
function applyObsInfo(obs: ObsInfo) {
  obsStatus.show(obs);
  editor.setObsScenes(obs.scenes); // при изменении модель сообщит «meta» → перерисуется блок привязки сцен
}

function connectWs() {
  const handle = createLiveHandler({
    setActive: (id) => editor.setActive(id),
    obs: applyObsInfo,
    scene: (name) => obsStatus.setScene(name),
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
  { ui: { toast, fail }, openStore: pluginStore.open, openSettings: () => $("settingsBtn").click() },
);
