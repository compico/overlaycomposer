import { test } from "node:test";
import assert from "node:assert/strict";
import { PluginHost } from "./pluginhost.ts";

// Мост без браузера: слушатель message перехватываем, iframe заменяем объектом с contentWindow.
function setup(info: { pluginId: string; widget?: string; sourceId?: string }) {
  let listener: (ev: { data: unknown; source: unknown }) => void = () => {};
  (globalThis as unknown as { addEventListener: unknown }).addEventListener = (_t: string, fn: typeof listener) => { listener = fn; };
  const sent: Record<string, unknown>[] = [];
  const host = new PluginHost({ send: (m) => sent.push(m), canSend: true });
  const replies: Record<string, unknown>[] = [];
  const win = { postMessage: (m: Record<string, unknown>) => replies.push(m) };
  const frame = { contentWindow: win } as unknown as HTMLIFrameElement;
  host.register(frame, { pluginId: info.pluginId, widget: info.widget, sourceId: info.sourceId ?? "src1", settings: {}, permissions: [] });
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
