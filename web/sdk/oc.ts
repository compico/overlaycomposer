/**
 * Клиентская часть API плагинов (собирается в /plugin-sdk/oc.js). Публичные типы — в oc.d.ts.
 * Работает поверх postMessage к странице оверлея (родителю iframe).
 */
import type { PluginInfo, ToHost, ToPlugin } from "./protocol.ts";

type Handler = (data: unknown) => void;

const hosted = window.parent !== window;
const handlers = new Map<string, Set<Handler>>();
const pending = new Map<number, { resolve: (v: unknown) => void; reject: (e: Error) => void }>();
let nextId = 1;

function post(msg: ToHost) {
  window.parent.postMessage(msg, "*"); // origin родителя из sandbox не узнать — он сам проверяет, кто прислал сообщение
}

let resolveReady!: (i: PluginInfo) => void;
let rejectReady!: (e: Error) => void;
const ready = new Promise<PluginInfo>((res, rej) => { resolveReady = res; rejectReady = rej; });
ready.catch(() => {}); // «не дождались» не должно попадать в unhandledrejection, если плагину ready не нужен

if (hosted) {
  addEventListener("message", (ev) => {
    if (ev.source !== window.parent) return;
    const m = ev.data as ToPlugin | null;
    if (!m || m.oc !== 1) return;
    switch (m.t) {
      case "ready":
        resolveReady(m.info);
        break;
      case "reply": {
        const p = pending.get(m.id);
        if (!p) return;
        pending.delete(m.id);
        if (m.ok) p.resolve(m.result);
        else p.reject(new Error(m.error));
        break;
      }
      case "event":
        for (const h of handlers.get(m.name) ?? []) {
          try { h(m.data); } catch (e) { console.error("oc: handler error", e); }
        }
        break;
    }
  });
  post({ oc: 1, t: "hello" });
  setTimeout(() => rejectReady(new Error("Overlay Composer did not answer")), 5000);
} else {
  rejectReady(new Error("not hosted by Overlay Composer"));
}

function call<T>(method: string, params?: unknown): Promise<T> {
  return ready.then(() => new Promise<T>((resolve, reject) => {
    const id = nextId++;
    const timer = setTimeout(() => {
      pending.delete(id);
      reject(new Error(`${method}: timeout`));
    }, 30000);
    pending.set(id, {
      resolve: (v) => { clearTimeout(timer); resolve(v as T); },
      reject: (e) => { clearTimeout(timer); reject(e); },
    });
    post({ oc: 1, t: "call", id, method, params });
  }));
}

function on(event: string, h: Handler): () => void {
  let set = handlers.get(`chat.${event}`);
  if (!set) handlers.set(`chat.${event}`, (set = new Set()));
  set.add(h);
  return () => off(event, h);
}
function off(event: string, h: Handler) {
  handlers.get(`chat.${event}`)?.delete(h);
}

function param<T extends string | number | boolean>(key: string, fallback: T): T {
  const raw = new URLSearchParams(location.search).get(key);
  if (raw === null) return fallback;
  if (typeof fallback === "number") {
    const n = parseFloat(raw);
    return (Number.isFinite(n) ? n : fallback) as T;
  }
  if (typeof fallback === "boolean") return (raw === "true" || raw === "1") as T;
  return raw as T;
}

const oc = {
  version: 1 as const,
  hosted,
  ready,
  param,
  chat: {
    subscribe: (options?: { channel?: string }) => call<{ channel: string }>("chat.subscribe", options ?? {}),
    unsubscribe: (channel: string) => call<void>("chat.unsubscribe", { channel }),
    say: (text: string, options?: { channel?: string; as?: string; replyTo?: string }) =>
      call<void>("chat.say", { text, ...options }),
    on,
    off,
  },
};

(window as unknown as { oc: typeof oc }).oc = oc;
