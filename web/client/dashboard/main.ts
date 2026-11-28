import { PluginHost } from "../shared/pluginhost.ts";
import { pluginFrameUrl } from "../shared/pluginkey.ts";

// Dashboard — окно управления стримом (issue #237). Панели — виджеты wasm-плагинов, объявивших область `dashboard`;
// раскладка хранится на сервере (/api/dashboard/layout). Страница только называет плагин и «виртуальный» источник
// dashboard:<виджет>; решает сервер (authorizePlugin).

interface Panel { id: string; plugin: string; widget: string; w: number; h: number }
interface Layout { rev: number; panels: Panel[] }
interface WidgetInfo { plugin: string; widget: string; name: string }
interface GridInfo { cols: number; maxRows: number; rowPx: number; maxPanels: number }
interface WasmInfo { meta: { id: string; permissions?: string[]; widgets?: { id: string }[] }; state: string; granted?: string[] }

const $ = <T extends HTMLElement>(id: string) => document.getElementById(id) as T;
const gridEl = $("grid");
const emptyEl = $("empty");
const msgEl = $("msg");
const addEl = $<HTMLSelectElement>("add");
const resetEl = $("reset");
const editEl = $("edit");

let ws: WebSocket | null = null;
function send(msg: Record<string, unknown>) {
  if (ws?.readyState === WebSocket.OPEN) ws.send(JSON.stringify(msg));
}
const plugins = new PluginHost({ send, canSend: true });

let layout: Layout = { rev: 0, panels: [] };
let widgets: WidgetInfo[] = [];
let grid: GridInfo = { cols: 12, maxRows: 8, rowPx: 120, maxPanels: 16 };
let infos: WasmInfo[] = [];
let editing = false;
let forbidden = false;
let saveTimer: number | undefined;
let dirty = false; // есть правки, ещё не ушедшие на сервер
let saving = false;

const clamp = (n: number, lo: number, hi: number) => Math.min(hi, Math.max(lo, n));
const widgetOf = (p: Panel) => widgets.find((w) => w.plugin === p.plugin && w.widget === p.widget);

function esc(s: string): string {
  return s.replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]!);
}

/** Что мешает показать панель: HTML подсказки (данные плагинов экранируются) либо "" — всё готово. */
function problem(p: Panel): string {
  if (forbidden) return "The dashboard widgets are shown on the computer where Overlay Composer runs.";
  const wi = widgetOf(p);
  if (!wi) return `The widget <b>${esc(p.plugin)}/${esc(p.widget)}</b> is not available. Install or update the plugin in the <a href="/editor">editor</a>.`;
  const info = infos.find((i) => i.meta.id === p.plugin);
  if (!info) return `The plugin <b>${esc(p.plugin)}</b> is not loaded.`;
  const missing = (info.meta.permissions ?? []).filter((perm) => !(info.granted ?? []).includes(perm));
  if (missing.length) return `Grant the plugin <b>${esc(p.plugin)}</b> the permissions <b>${missing.map(esc).join("</b>, <b>")}</b> in the <a href="/editor">editor</a> (Plugins).`;
  if (info.state !== "running") return `The plugin <b>${esc(p.plugin)}</b> is not running (${esc(info.state)}). Restart it in the <a href="/editor">editor</a> (Plugins).`;
  return "";
}

// ---------- панели ----------
interface View { el: HTMLElement; body: HTMLElement; frame: HTMLIFrameElement | null; key: string }
const views = new Map<string, View>();

function mountFrame(v: View, p: Panel, granted: string[]) {
  if (v.frame) return;
  const url = pluginFrameUrl({ plugin: p.plugin, widget: p.widget, settings: {} });
  if (!url) return;
  const f = document.createElement("iframe");
  // Плагин — чужой код: iframe без allow-same-origin не получает доступа к API приложения.
  f.setAttribute("sandbox", "allow-scripts allow-forms allow-popups allow-modals");
  f.src = url;
  v.body.replaceChildren(f);
  v.frame = f;
  plugins.register(f, { pluginId: p.plugin, widget: p.widget, sourceId: `dashboard:${p.widget}`, surface: "dashboard", settings: {}, permissions: granted });
}

function unmountFrame(v: View) {
  if (!v.frame) return;
  plugins.unregister(v.frame);
  v.frame.remove();
  v.frame = null;
}

function btn(label: string, title: string, on: () => void, disabled = false): HTMLButtonElement {
  const b = document.createElement("button");
  b.textContent = label;
  b.title = title;
  b.disabled = disabled;
  b.onclick = on;
  return b;
}

function buildView(p: Panel): View {
  const el = document.createElement("section");
  el.className = "panel";
  const h = document.createElement("h2");
  const body = document.createElement("div");
  body.className = "body";
  el.append(h, body);
  return { el, body, frame: null, key: "" };
}

function renderPanels() {
  const ids = new Set(layout.panels.map((p) => p.id));
  for (const [id, v] of views) {
    if (!ids.has(id)) {
      unmountFrame(v);
      v.el.remove();
      views.delete(id);
    }
  }
  layout.panels.forEach((p, i) => {
    let v = views.get(p.id);
    if (!v) {
      v = buildView(p);
      views.set(p.id, v);
      gridEl.append(v.el);
    }
    // Порядок — через CSS order: перестановка DOM перезагружала бы iframe.
    v.el.style.order = String(i);
    v.el.style.gridColumn = `span ${clamp(p.w, 1, grid.cols)}`;
    v.el.style.gridRow = `span ${clamp(p.h, 1, grid.maxRows)}`;

    const wi = widgetOf(p);
    const head = v.el.firstElementChild as HTMLElement;
    const key = `${editing}|${wi?.name ?? ""}|${i}|${p.w}|${p.h}|${layout.panels.length}`;
    if (v.key !== key) {
      v.key = key;
      const name = document.createElement("span");
      name.className = "name";
      name.textContent = wi?.name ?? `${p.plugin}/${p.widget}`;
      head.replaceChildren(name);
      if (editing) {
        const n = layout.panels.length;
        head.append(
          btn("◀", "Move earlier", () => move(i, -1), i === 0),
          btn("▶", "Move later", () => move(i, 1), i === n - 1),
          btn("W−", "Narrower", () => resize(p, -1, 0), p.w <= 1),
          btn("W+", "Wider", () => resize(p, 1, 0), p.w >= grid.cols),
          btn("H−", "Shorter", () => resize(p, 0, -1), p.h <= 1),
          btn("H+", "Taller", () => resize(p, 0, 1), p.h >= grid.maxRows),
          btn("✕", "Remove", () => remove(p)),
        );
      }
    }

    const prob = problem(p);
    if (prob) {
      unmountFrame(v);
      const d = document.createElement("div");
      d.className = "hint";
      d.innerHTML = prob; // только наши константы и экранированные данные плагинов
      v.body.replaceChildren(d);
    } else {
      const granted = infos.find((x) => x.meta.id === p.plugin)?.granted ?? [];
      mountFrame(v, p, granted);
    }
  });
  emptyEl.hidden = layout.panels.length > 0 || editing;
  renderAdd();
}

function renderAdd() {
  addEl.hidden = resetEl.hidden = !editing;
  if (!editing) return;
  const free = widgets;
  addEl.replaceChildren(new Option("Add widget…", ""));
  for (const w of free) addEl.append(new Option(`${w.name} (${w.plugin})`, `${w.plugin}/${w.widget}`));
  addEl.disabled = layout.panels.length >= grid.maxPanels || free.length === 0;
}

// ---------- правка раскладки ----------
function changed() {
  dirty = true;
  renderPanels();
  clearTimeout(saveTimer);
  saveTimer = window.setTimeout(() => void save(), 400);
}

function move(i: number, d: number) {
  const j = i + d;
  if (j < 0 || j >= layout.panels.length) return;
  [layout.panels[i], layout.panels[j]] = [layout.panels[j], layout.panels[i]];
  changed();
}

function resize(p: Panel, dw: number, dh: number) {
  p.w = clamp(p.w + dw, 1, grid.cols);
  p.h = clamp(p.h + dh, 1, grid.maxRows);
  changed();
}

function remove(p: Panel) {
  layout.panels = layout.panels.filter((x) => x !== p);
  changed();
}

function addPanel(key: string) {
  const [plugin, widget] = key.split("/");
  if (!plugin || !widget || layout.panels.length >= grid.maxPanels) return;
  const base = `${plugin}-${widget}`;
  let id = base;
  for (let n = 2; layout.panels.some((x) => x.id === id); n++) id = `${base}-${n}`;
  layout.panels.push({ id, plugin, widget, w: 6, h: 6 });
  changed();
}

async function save() {
  if (saving) {
    saveTimer = window.setTimeout(() => void save(), 400);
    return;
  }
  saving = true;
  try {
    const res = await fetch("/api/dashboard/layout", {
      method: "PUT",
      headers: { "Content-Type": "application/json", "X-Overlay-Composer": "1" },
      body: JSON.stringify(layout),
    });
    if (res.status === 409) {
      note("The layout was changed elsewhere — reloaded.");
      dirty = false;
      await load();
    } else if (!res.ok) {
      note(`Could not save the layout (${res.status}).`);
    } else {
      layout.rev = ((await res.json()) as { rev: number }).rev;
      dirty = false;
      note("Saved.");
    }
  } catch {
    note("Could not reach Overlay Composer.");
  } finally {
    saving = false;
  }
}

async function reset() {
  clearTimeout(saveTimer);
  dirty = false;
  try {
    const res = await fetch("/api/dashboard/layout", { method: "DELETE", headers: { "X-Overlay-Composer": "1" } });
    if (!res.ok) return note(`Could not reset the layout (${res.status}).`);
  } catch {
    return note("Could not reach Overlay Composer.");
  }
  await load();
  note("Reset to default.");
}

let noteTimer: number | undefined;
function note(text: string) {
  msgEl.textContent = text;
  clearTimeout(noteTimer);
  noteTimer = window.setTimeout(() => (msgEl.textContent = ""), 3000);
}

function normalize(l: Partial<Layout>): Layout {
  return { rev: l.rev ?? 0, panels: l.panels ?? [] };
}

// ---------- загрузка ----------
async function load() {
  try {
    const res = await fetch("/api/dashboard/layout");
    if (!res.ok) return;
    const data = (await res.json()) as { layout: Layout; widgets: WidgetInfo[]; grid: GridInfo };
    // Правки, ещё не ушедшие на сервер, не затираем.
    if (!dirty) layout = normalize(data.layout);
    widgets = data.widgets ?? [];
    grid = data.grid ?? grid;
    gridEl.style.setProperty("--row", `${grid.rowPx}px`);
  } catch {
    return;
  }
  await loadPlugins();
}

async function loadPlugins() {
  try {
    const res = await fetch("/api/wasmplugins");
    forbidden = res.status === 403;
    infos = res.ok ? (((await res.json()) as { plugins?: WasmInfo[] }).plugins ?? []) : [];
  } catch {
    return;
  }
  renderPanels();
}

editEl.onclick = () => {
  editing = !editing;
  editEl.textContent = editing ? "Done" : "Edit layout";
  editEl.classList.toggle("on", editing);
  renderPanels();
};
addEl.onchange = () => {
  if (addEl.value) addPanel(addEl.value);
  addEl.value = "";
};
resetEl.onclick = () => void reset();

// ---------- связь с сервером ----------
function connect() {
  ws = new WebSocket(`${location.protocol === "https:" ? "wss" : "ws"}://${location.host}/ws`);
  const mine = ws;
  mine.onopen = () => {
    msgEl.textContent = "";
    plugins.onConnected();
    void load();
  };
  mine.onmessage = (ev) => {
    const msg = JSON.parse(ev.data) as { type: string };
    if (plugins.onServerMessage(msg)) return;
    if (msg.type === "hello") void load();
  };
  mine.onclose = () => {
    msgEl.textContent = "Reconnecting…";
    setTimeout(connect, 1500);
  };
}

connect();
// Плагин могли поставить или выдать ему права в другой вкладке: проверяем раз в несколько секунд, пока есть панели с проблемой,
// и при возврате на вкладку.
setInterval(() => { if (!editing && layout.panels.some((p) => problem(p))) void load(); }, 4000);
addEventListener("focus", () => { if (!editing) void load(); });
