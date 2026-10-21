import type { ClientMessage, Layout, ServerMessage, Source } from "../../shared/types.ts";
import { PluginHost } from "../shared/pluginhost.ts";
import { OverlayScene } from "../shared/scene.ts";

const stage = document.getElementById("stage")!;
// Мост плагин↔сервер: чат Twitch приходит по общему /ws, ответы плагинов уходят тем же путём.
const plugins = new PluginHost({ send: (m) => send(m as unknown as ClientMessage), canSend: true });
const scene = new OverlayScene(stage, { interactive: false, plugins });

let currentLayoutId: string | null = null;

async function loadSources() {
  const sources: Source[] = await (await fetch("/api/sources")).json();
  scene.setSources(sources);
  hud("sources", String(sources.length));
}

async function loadLayout(id: string | null) {
  if (!id) return;
  const res = await fetch(`/api/layouts/${encodeURIComponent(id)}`);
  if (!res.ok) return;
  const layout: Layout = await res.json();
  currentLayoutId = layout.id;
  scene.setLayout(layout);
  scene.fit("contain");
  const visible = layout.objects.filter((o) => o.visible).length;
  hud("layout", `${layout.name} (${layout.id}): ${visible}/${layout.objects.length} visible, canvas ${layout.canvas.width}×${layout.canvas.height}`);
  clientLog("info", `layout loaded: ${layout.id}, objects ${layout.objects.length}, visible ${visible}, window ${innerWidth}×${innerHeight}`);
}

addEventListener("resize", () => scene.fit("contain"));

// ---------- OBS: сцена приходит от самого OBS Browser Source (JS API obs-browser) ----------
//
// Внутри OBS страница получает объект window.obsstudio. getControlLevel() доступен всегда,
// getCurrentScene()/getScenes() и событие obsSceneChanged — при Page permissions >= 2
// («Read access to user information»). В обычном браузере (тест в Firefox/Chrome) объекта нет —
// тогда ничего не сообщаем, и layout переключается вручную из редактора (кнопка ▶).

interface ObsStudio {
  getControlLevel?: (cb: (level: number) => void) => void;
  getCurrentScene?: (cb: (scene: { name: string }) => void) => void;
  getScenes?: (cb: (scenes: string[]) => void) => void;
}
const obsstudio = (globalThis as unknown as { obsstudio?: ObsStudio }).obsstudio;


// ---------- диагностика ----------
//
// В OBS нет удобных DevTools, поэтому overlay сам сообщает серверу о старте и ошибках (они попадают в лог приложения,
// «Open log folder» в настройках). Открытие /overlay?debug=1 показывает ещё и плашку со статусом прямо на странице.

const DEBUG = new URLSearchParams(location.search).has("debug");
const hudLines = new Map<string, string>();
let hudEl: HTMLElement | null = null;

function hud(key: string, value: string) {
  if (!DEBUG) return;
  hudLines.set(key, value);
  if (!hudEl) {
    hudEl = document.createElement("pre");
    hudEl.style.cssText =
      "position:fixed;left:6px;top:6px;z-index:2147483647;margin:0;padding:6px 8px;font:12px/1.35 monospace;" +
      "color:#0f0;background:rgba(0,0,0,.75);max-width:90vw;white-space:pre-wrap;pointer-events:none";
    document.body.append(hudEl);
  }
  hudEl.textContent = [...hudLines].map(([k, v]) => `${k}: ${v}`).join("\n");
}

let logBudget = 40; // не заваливаем сервер, если ошибка сыплется в цикле
function clientLog(level: "info" | "error", msg: string) {
  if (logBudget-- <= 0) return;
  fetch("/api/client-log", {
    method: "POST",
    headers: { "content-type": "application/json", "x-overlay-composer": "1" },
    body: JSON.stringify({ level, msg: msg.slice(0, 1000), ua: navigator.userAgent }),
  }).catch(() => {});
}

addEventListener("error", (e) => {
  const m = `${e.message} @ ${e.filename}:${e.lineno}:${e.colno}`;
  clientLog("error", m);
  hud("error", m);
});
addEventListener("unhandledrejection", (e) => {
  const m = `unhandled rejection: ${e.reason instanceof Error ? e.reason.stack ?? e.reason.message : String(e.reason)}`;
  clientLog("error", m);
  hud("error", m);
});

let ws: WebSocket | null = null;
function send(msg: ClientMessage) {
  if (ws?.readyState === WebSocket.OPEN) ws.send(JSON.stringify(msg));
}

function reportScenes() {
  obsstudio?.getScenes?.((scenes) => {
    if (Array.isArray(scenes)) send({ type: "obs:report", scenes });
  });
}

/** Сообщить серверу, что мы внутри OBS, и передать текущую сцену. */
function announceToServer() {
  if (!obsstudio) return;
  const hello = (level: number | null) => {
    send({ type: "overlay:hello", inObs: true, permissionLevel: level });
    obsstudio.getCurrentScene?.((s) => {
      if (s?.name) send({ type: "obs:report", scene: s.name });
    });
    reportScenes();
  };
  if (obsstudio.getControlLevel) obsstudio.getControlLevel((level) => hello(typeof level === "number" ? level : null));
  else hello(null);
}

if (obsstudio) {
  addEventListener("obsSceneChanged", (e) => {
    const name = (e as CustomEvent<{ name?: string }>).detail?.name;
    if (name) send({ type: "obs:report", scene: name });
  });
  addEventListener("obsSceneListChanged", reportScenes);
}

// ---------- связь с сервером ----------

function connect() {
  ws = new WebSocket(`${location.protocol === "https:" ? "wss" : "ws"}://${location.host}/ws`);
  const mine = ws;

  mine.onopen = () => {
    hud("ws", "connected");
    plugins.onConnected();
    announceToServer();
  };

  mine.onmessage = async (ev) => {
    const msg = JSON.parse(ev.data) as ServerMessage;
    if (plugins.onServerMessage(msg as unknown as { type: string })) return;
    hud("last msg", msg.type);
    switch (msg.type) {
      case "hello":
        await loadSources();
        await loadLayout(msg.layoutId);
        break;
      case "scene:changed":
        if (msg.layoutId) await loadLayout(msg.layoutId);
        break;
      case "layout:updated":
        if (msg.layoutId === currentLayoutId) await loadLayout(msg.layoutId);
        break;
      case "sources:changed":
        await loadSources();
        break;
      case "layouts:changed":
        // Активный layout мог быть удалён — hello при переподключении не придёт,
        // поэтому проверяем сами.
        if (currentLayoutId && !(await fetch(`/api/layouts/${currentLayoutId}`)).ok) {
          const st = await (await fetch("/api/state")).json();
          await loadLayout(st.activeLayoutId);
        }
        break;
    }
  };

  mine.onclose = () => {
    hud("ws", "closed, reconnecting…");
    setTimeout(connect, 1500);
  };
}

hud("start", `${innerWidth}×${innerHeight}, obsstudio=${obsstudio ? "yes" : "no"}`);
clientLog("info", `overlay started: window ${innerWidth}×${innerHeight}, obsstudio=${obsstudio ? "yes" : "no"}, ${navigator.userAgent}`);
connect();
