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
      case "stopping":
        void runStop(m.graceMs);
        break;
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

function subscribe(name: string, h: Handler): () => void {
  let set = handlers.get(name);
  if (!set) handlers.set(name, (set = new Set()));
  set.add(h);
  return () => off(name, h);
}
function off(name: string, h: Handler) {
  handlers.get(name)?.delete(h);
}

interface GetResult { found: boolean; value: unknown; rev: number }

const updateQueues = new Map<string, Promise<unknown>>();

/**
 * Прочитал → изменил → записал с проверкой ревизии: если ключ тем временем изменила другая копия, повторяем с новым значением.
 * Вызовы update для одного ключа внутри одной страницы идут по очереди — так они не конфликтуют друг с другом.
 */
function update<T>(key: string, fn: (cur: T | undefined) => T): Promise<T> {
  const run = async (): Promise<T> => {
    for (let i = 0; ; i++) {
      const cur = await call<GetResult>("storage.get", { key });
      const next = fn(cur.found ? (cur.value as T) : undefined);
      try {
        await call("storage.set", { key, value: next, ifRev: cur.rev });
        return next;
      } catch (e) {
        if (!(e instanceof Error) || !e.message.startsWith("conflict") || i >= 30) throw e;
        await new Promise((r) => setTimeout(r, Math.random() * 20 * (i + 1))); // разводим конкурентов по времени
      }
    }
  };
  const prev = updateQueues.get(key) ?? Promise.resolve();
  const p = prev.then(run, run);
  const tail = p.catch(() => {});
  updateQueues.set(key, tail);
  tail.then(() => { if (updateQueues.get(key) === tail) updateQueues.delete(key); });
  return p;
}

// ---- остановка приложения ----

type StopHandler = (e: { reason: "shutdown"; graceMs: number }) => void | Promise<void>;
const stopHandlers = new Set<StopHandler>();
let stopping = false;

/** Приложение выключается: вызываем обработчики (параллельно), ждём не дольше graceMs и сообщаем странице, что закончили. */
async function runStop(graceMs: number) {
  if (stopping) return;
  stopping = true;
  const budget = Math.max(0, graceMs - 200); // запас на передачу ответа странице
  const run = Promise.allSettled([...stopHandlers].map(async (h) => {
    try { await h({ reason: "shutdown", graceMs }); } catch (e) { console.error("oc: onStop handler error", e); }
  }));
  await Promise.race([run, new Promise((r) => setTimeout(r, budget))]);
  post({ oc: 1, t: "stopped" });
}

function onStop(h: StopHandler): () => void {
  stopHandlers.add(h);
  if (hosted) post({ oc: 1, t: "hook", hook: "stop" }); // повторная отправка безвредна
  return () => { stopHandlers.delete(h); };
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
  lifecycle: { onStop },
  chat: {
    subscribe: (options?: { channel?: string }) => call<{ channel: string }>("chat.subscribe", options ?? {}),
    unsubscribe: (channel: string) => call<void>("chat.unsubscribe", { channel }),
    say: (text: string, options?: { channel?: string; as?: string; replyTo?: string }) =>
      call<void>("chat.say", { text, ...options }),
    on: (event: string, h: Handler) => subscribe(`chat.${event}`, h),
    off: (event: string, h: Handler) => off(`chat.${event}`, h),
  },
  net: {
    localGet: (target: string, path: string) => call<{ status: number; body: string }>("net.localGet", { target, path }),
  },
  storage: {
    get: async (key: string) => {
      const r = await call<GetResult>("storage.get", { key });
      return r.found ? r.value : undefined;
    },
    set: async (key: string, value: unknown) => {
      if (value === undefined) throw new Error("undefined cannot be stored (use delete)");
      await call("storage.set", { key, value });
    },
    delete: async (key: string) => (await call<{ deleted: boolean }>("storage.delete", { key })).deleted,
    list: async (prefix?: string, limit?: number) =>
      (await call<{ entries: { key: string; value: unknown }[] }>("storage.list", { prefix, limit })).entries
        .map((e) => ({ key: e.key, value: e.value })),
    update,
    onChange: (h: Handler) => subscribe("storage.change", h),
  },
};

(window as unknown as { oc: typeof oc }).oc = oc;
