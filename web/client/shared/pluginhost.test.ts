import { test } from "node:test";
import assert from "node:assert/strict";
import { PluginHost } from "./pluginhost.ts";

// Мост без браузера: слушатель message перехватываем, iframe заменяем объектом с contentWindow.
function setup(info: { pluginId: string; widget?: string; sourceId?: string; page?: "settings" }) {
  let listener: (ev: { data: unknown; source: unknown }) => void = () => {};
  (globalThis as unknown as { addEventListener: unknown }).addEventListener = (_t: string, fn: typeof listener) => { listener = fn; };
  const sent: Record<string, unknown>[] = [];
  const host = new PluginHost({ send: (m) => sent.push(m), canSend: true });
  const replies: Record<string, unknown>[] = [];
  const win = { postMessage: (m: Record<string, unknown>) => replies.push(m) };
  const frame = { contentWindow: win } as unknown as HTMLIFrameElement;
  host.register(frame, { pluginId: info.pluginId, widget: info.widget, sourceId: info.sourceId ?? "src1", settings: {}, permissions: [], page: info.page });
  let id = 0;
  const call = async (method: string, params: unknown) => {
    const before = replies.length;
    listener({ data: { oc: 1, t: "call", id: ++id, method, params }, source: win });
    // ответ виджету приходит асинхронно (после ответа сервера): ждём, пока он появится
    for (let i = 0; i < 100 && replies.length === before; i++) await new Promise((r) => setTimeout(r, 2));
    return { reply: replies[before] as Record<string, unknown> | undefined };
  };
  return { host, sent, replies, call, listener: () => listener, win };
}

test("plugin.call: запрос уходит серверу с плагином и источником виджета, а не с тем, что прислал виджет", async () => {
  const t = setup({ pluginId: "music", widget: "queue", sourceId: "src-7" });
  const p = t.call("plugin.call", { method: "POST", path: "/started", query: { a: 1, b: "x", c: true }, body: { id: "1" }, plugin: "evil", source: "other" });
  await new Promise((r) => setTimeout(r, 0));
  assert.equal(t.sent.length, 1);
  const msg = t.sent[0];
  assert.deepEqual({ ...msg, reqId: undefined }, {
    type: "plugin:call", plugin: "music", source: "src-7", method: "POST", path: "/started",
    query: { a: 1, b: "x", c: true }, body: { id: "1" }, reqId: undefined,
  });
  t.host.onServerMessage({ type: "plugin:result", reqId: msg.reqId, ok: true, status: 200, body: { ok: 1 } });
  const { reply } = await p;
  assert.deepEqual(reply, { oc: 1, t: "reply", id: 1, ok: true, result: { status: 200, body: { ok: 1 } } });
});

test("plugin.call: пустое тело ответа становится null, ошибка сервера — ошибкой для виджета", async () => {
  const t = setup({ pluginId: "music", widget: "queue" });
  const p1 = t.call("plugin.call", { method: "GET", path: "/queue" });
  await new Promise((r) => setTimeout(r, 0));
  t.host.onServerMessage({ type: "plugin:result", reqId: t.sent[0].reqId, ok: true, status: 204 });
  assert.deepEqual((await p1).reply?.result, { status: 204, body: null });

  const p2 = t.call("plugin.call", { method: "GET", path: "/queue" });
  await new Promise((r) => setTimeout(r, 0));
  t.host.onServerMessage({ type: "plugin:result", reqId: t.sent[1].reqId, ok: false, error: "no such route" });
  const { reply } = await p2;
  assert.equal(reply?.ok, false);
  assert.equal(reply?.error, "no such route");
});

test("plugin.call, sad paths: не виджет автономного плагина, плохие параметры — серверу ничего не уходит", async () => {
  const html = setup({ pluginId: "twitch-chat" }); // обычный HTML-плагин
  assert.equal((await html.call("plugin.call", { method: "GET", path: "/x" })).reply?.ok, false);
  assert.equal(html.sent.length, 0);

  const t = setup({ pluginId: "music", widget: "queue" });
  const bad: Record<string, unknown>[] = [
    {}, { method: "GET" }, { path: "/x" },
    { method: "GET".repeat(5), path: "/x" },
    { method: "GET", path: "/" + "a".repeat(256) },
    { method: "GET", path: "/x", query: [1] },
    { method: "GET", path: "/x", query: { a: { b: 1 } } },
    { method: "GET", path: "/x", query: { a: null } },
    { method: "GET", path: "/x", query: Object.fromEntries(Array.from({ length: 33 }, (_, i) => [`k${i}`, "v"])) },
  ];
  for (const params of bad) {
    const { reply } = await t.call("plugin.call", params);
    assert.equal(reply?.ok, false, JSON.stringify(params).slice(0, 60));
  }
  assert.equal(t.sent.length, 0);
});

test("plugin.call от чужого окна игнорируется", async () => {
  const t = setup({ pluginId: "music", widget: "queue" });
  t.listener()({ data: { oc: 1, t: "call", id: 1, method: "plugin.call", params: { method: "GET", path: "/x" } }, source: { postMessage() {} } });
  await new Promise((r) => setTimeout(r, 0));
  assert.equal(t.sent.length, 0);
  assert.equal(t.replies.length, 0);
});

test("plugin.call со страницы настроек: источника нет, плагин берётся из регистрации, а не из сообщения", async () => {
  const t = setup({ pluginId: "music", sourceId: "", page: "settings" });
  const p = t.call("plugin.call", { method: "GET", path: "/bans", plugin: "evil", source: "other" });
  await new Promise((r) => setTimeout(r, 0));
  assert.equal(t.sent.length, 1);
  assert.deepEqual({ ...t.sent[0], reqId: undefined }, { type: "plugin:call", plugin: "music", source: "", method: "GET", path: "/bans", query: undefined, body: undefined, reqId: undefined });
  t.host.onServerMessage({ type: "plugin:result", reqId: t.sent[0].reqId, ok: true, status: 200, body: { items: [] } });
  assert.equal((await p).reply?.ok, true);
});

// Несколько окон на одном хосте: подписки на события плагина.
function multi() {
  let listener: (ev: { data: unknown; source: unknown }) => void = () => {};
  (globalThis as unknown as { addEventListener: unknown }).addEventListener = (_t: string, fn: typeof listener) => { listener = fn; };
  const sent: Record<string, unknown>[] = [];
  const host = new PluginHost({ send: (m) => sent.push(m), canSend: true });
  const mk = (info: { pluginId: string; widget?: string; sourceId?: string; page?: "settings" }) => {
    const replies: Record<string, unknown>[] = [];
    const win = { postMessage: (m: Record<string, unknown>) => replies.push(m) };
    const frame = { contentWindow: win } as unknown as HTMLIFrameElement;
    host.register(frame, { pluginId: info.pluginId, widget: info.widget, sourceId: info.sourceId ?? "src1", settings: {}, permissions: [], page: info.page });
    let id = 0;
    const call = (method: string, params: unknown) => {
      const my = ++id;
      listener({ data: { oc: 1, t: "call", id: my, method, params }, source: win });
      return async () => {
        for (let i = 0; i < 100 && !replies.some((r) => r.t === "reply" && r.id === my); i++) await new Promise((r) => setTimeout(r, 2));
        return replies.find((r) => r.t === "reply" && r.id === my) as Record<string, unknown>;
      };
    };
    return { frame, replies, call };
  };
  const answer = (type: string, ok = true, error?: string) => {
    const last = [...sent].reverse().find((m) => m.type === type)!;
    host.onServerMessage({ type: `${type}:result`, reqId: last.reqId, ok, error });
  };
  return { host, sent, mk, answer };
}
const events = (w: { replies: Record<string, unknown>[] }) => w.replies.filter((r) => r.t === "event" && r.name === "plugin.event");

test("plugin.subscribe: сервер получает плагин и источник окна, события идут только подписанным окнам этого плагина", async () => {
  const t = multi();
  const a = t.mk({ pluginId: "music", widget: "queue", sourceId: "s1" });
  const b = t.mk({ pluginId: "music", widget: "player", sourceId: "s2" });
  const other = t.mk({ pluginId: "other", widget: "w", sourceId: "s3" });
  const wa = a.call("plugin.subscribe", { topic: "now", plugin: "evil", source: "x" });
  await new Promise((r) => setTimeout(r, 0));
  assert.deepEqual({ ...t.sent[0], reqId: undefined }, { type: "plugin:sub", plugin: "music", source: "s1", topic: "now", reqId: undefined });
  t.answer("plugin:sub");
  assert.equal((await wa()).ok, true);
  // второе окно того же плагина: отдельной серверной подписки не нужно
  const wb = b.call("plugin.subscribe", { topic: "now" });
  assert.equal((await wb()).ok, true);
  assert.equal(t.sent.filter((m) => m.type === "plugin:sub").length, 1);
  assert.equal(t.host.onServerMessage({ type: "plugin:event", plugin: "music", topic: "now", data: { n: 1 }, seq: 5, retained: true }), true);
  assert.deepEqual(events(a)[0], { oc: 1, t: "event", name: "plugin.event", data: { topic: "now", data: { n: 1 }, seq: 5, retained: true } });
  assert.equal(events(b).length, 1);
  assert.equal(events(other).length, 0, "другой плагин событий не получает");
  // событие другого топика окну без подписки не доходит
  t.host.onServerMessage({ type: "plugin:event", plugin: "music", topic: "queue", data: 1, seq: 1 });
  assert.equal(events(a).length, 1);
  // серверная подписка снимается, когда ушло последнее окно
  t.host.unregister(a.frame);
  assert.equal(t.sent.filter((m) => m.type === "plugin:unsub").length, 0);
  t.host.unregister(b.frame);
  assert.deepEqual(t.sent.filter((m) => m.type === "plugin:unsub"), [{ type: "plugin:unsub", plugin: "music", topic: "now" }]);
});

test("plugin.subscribe: плохой топик, чужой тип окна и отказ сервера", async () => {
  const t = multi();
  const w = t.mk({ pluginId: "music", widget: "queue" });
  for (const topic of ["", "Now", "a b", "x".repeat(65), 7, "a/b"]) {
    const r = await w.call("plugin.subscribe", { topic })();
    assert.equal(r.ok, false, String(topic));
  }
  assert.equal(t.sent.length, 0, "плохие топики до сервера не доходят");
  const plain = t.mk({ pluginId: "music" }); // не виджет плагина и не страница настроек
  assert.equal((await plain.call("plugin.subscribe", { topic: "now" })()).ok, false);
  const p = w.call("plugin.subscribe", { topic: "secret" });
  await new Promise((r) => setTimeout(r, 0));
  t.answer("plugin:sub", false, "unknown topic");
  const r = await p();
  assert.deepEqual({ ok: r.ok, error: r.error }, { ok: false, error: "unknown topic" });
  t.host.unregister(w.frame);
  assert.equal(t.sent.filter((m) => m.type === "plugin:unsub").length, 0, "подписка не состоялась: снимать нечего");
});

test("plugin.subscribe: после переподключения /ws подписки восстанавливаются, плагин.unsubscribe снимает", async () => {
  const t = multi();
  const s = t.mk({ pluginId: "music", page: "settings", sourceId: "" });
  const p = s.call("plugin.subscribe", { topic: "now" });
  await new Promise((r) => setTimeout(r, 0));
  t.answer("plugin:sub");
  await p();
  t.sent.length = 0;
  t.host.onConnected();
  assert.deepEqual(t.sent.map((m) => ({ ...m, reqId: undefined })), [{ type: "plugin:sub", plugin: "music", source: "", topic: "now", reqId: undefined }]);
  await s.call("plugin.unsubscribe", { topic: "now" })();
  assert.deepEqual(t.sent.at(-1), { type: "plugin:unsub", plugin: "music", topic: "now" });
  t.sent.length = 0;
  t.host.onConnected();
  assert.equal(t.sent.length, 0);
  t.host.onServerMessage({ type: "plugin:event", plugin: "music", topic: "now", data: 1, seq: 1 });
  assert.equal(events(s).length, 0, "после unsubscribe события не доставляются");
});
