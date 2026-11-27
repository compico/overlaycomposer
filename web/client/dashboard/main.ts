import { PluginHost } from "../shared/pluginhost.ts";
import { pluginFrameUrl } from "../shared/pluginkey.ts";

// Dashboard — окно управления стримом (issue #237). Пока в нём чат: виджет wasm-плагина `chat` в iframe, как в сцене OBS.
// Страница только называет плагин и «виртуальный» источник dashboard:<виджет>; решает сервер (authorizePlugin).

const CHAT_PLUGIN = "chat";
const CHAT_WIDGET = "chat";
// Нужные плагину права: без них виджет не получит ни историю, ни новые сообщения.
const CHAT_PERMS = ["chat.read", "events.publish"];
// Настройки виджета для окна: читаемый размер, без прозрачности и затухания (в OBS по умолчанию другие).
const CHAT_SETTINGS: Record<string, string | number | boolean> = {
  maxMessages: 200, fadeSeconds: 0, direction: "bottom", msgSize: 16, nickSize: 16, msgColor: "#e8e8ec", shadow: false, gap: 4, padding: 4,
};

const box = document.getElementById("chatBox")!;
const hint = document.getElementById("chatHint")!;
const conn = document.getElementById("conn")!;

let ws: WebSocket | null = null;
function send(msg: Record<string, unknown>) {
  if (ws?.readyState === WebSocket.OPEN) ws.send(JSON.stringify(msg));
}
const plugins = new PluginHost({ send, canSend: true });

interface WasmInfo { meta: { id: string; widgets?: { id: string }[] }; state: string; granted?: string[] }

let frame: HTMLIFrameElement | null = null;

function showHint(html: string) {
  hint.hidden = false;
  hint.replaceChildren();
  const t = document.createElement("div");
  t.innerHTML = html; // только наши константы, данных плагинов здесь нет
  hint.append(t);
}

/** Что мешает показать чат: текст подсказки, либо "" — всё готово. */
function chatProblem(list: WasmInfo[]): string {
  const p = list.find((i) => i.meta.id === CHAT_PLUGIN);
  if (!p) return `The <b>Twitch chat</b> plugin is not installed. Install it from the plugin store in the <a href="/editor">editor</a>.`;
  if (!(p.meta.widgets ?? []).some((w) => w.id === CHAT_WIDGET)) return "The chat plugin has no chat widget: update the plugin.";
  const missing = CHAT_PERMS.filter((perm) => !(p.granted ?? []).includes(perm));
  if (missing.length) return `Grant the chat plugin the permissions <b>${missing.join("</b>, <b>")}</b> in the <a href="/editor">editor</a> (Plugins).`;
  if (p.state !== "running") return `The chat plugin is not running (${p.state}). Restart it in the <a href="/editor">editor</a> (Plugins).`;
  return "";
}

function showChat() {
  if (frame) return;
  const src = { plugin: CHAT_PLUGIN, widget: CHAT_WIDGET, settings: CHAT_SETTINGS };
  const url = pluginFrameUrl(src);
  if (!url) return;
  frame = document.createElement("iframe");
  // Плагин — чужой код: iframe без allow-same-origin не получает доступа к API приложения.
  frame.setAttribute("sandbox", "allow-scripts allow-forms allow-popups allow-modals");
  frame.src = url;
  box.append(frame);
  hint.hidden = true;
  plugins.register(frame, { pluginId: CHAT_PLUGIN, widget: CHAT_WIDGET, sourceId: `dashboard:${CHAT_WIDGET}`, settings: CHAT_SETTINGS, permissions: CHAT_PERMS });
}

function hideChat() {
  if (!frame) return;
  plugins.unregister(frame);
  frame.remove();
  frame = null;
}

async function refresh() {
  try {
    const res = await fetch("/api/wasmplugins");
    if (!res.ok) {
      hideChat();
      showHint(res.status === 403 ? "The chat is shown on the computer where Overlay Composer runs." : `Could not load plugins (${res.status}).`);
      return;
    }
    const data = (await res.json()) as { plugins: WasmInfo[] };
    const problem = chatProblem(data.plugins ?? []);
    if (problem) {
      hideChat();
      showHint(problem);
    } else showChat();
  } catch {
    showHint("Could not reach Overlay Composer.");
  }
}

// ---------- связь с сервером ----------
function connect() {
  ws = new WebSocket(`${location.protocol === "https:" ? "wss" : "ws"}://${location.host}/ws`);
  const mine = ws;
  mine.onopen = () => {
    conn.textContent = "";
    plugins.onConnected();
    void refresh();
  };
  mine.onmessage = (ev) => {
    const msg = JSON.parse(ev.data) as { type: string };
    if (plugins.onServerMessage(msg)) return;
    if (msg.type === "hello") void refresh();
  };
  mine.onclose = () => {
    conn.textContent = "Reconnecting…";
    setTimeout(connect, 1500);
  };
}

connect();
// Плагин могли поставить или выдать ему права в другой вкладке: проверяем раз в несколько секунд, пока чата нет, и при возврате на вкладку.
setInterval(() => { if (!frame) void refresh(); }, 4000);
addEventListener("focus", () => void refresh());
