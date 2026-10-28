// node --experimental-strip-types --test web/client/editor/features/notifications/model.test.ts
import assert from "node:assert/strict";
import { test } from "node:test";
import type { Api } from "../../lib/api.ts";
import { createNotificationsModel, type NotifState } from "./model.ts";

const item = (key: string, read = false) => ({ key, kind: "plugin-update", title: key, at: 1, read });
const state = (keys: string[], unread: number): NotifState => ({ items: keys.map((k) => item(k)), unread, lastCheck: 0 });

function fakeApi(responses: Record<string, unknown>) {
  const calls: { path: string; method?: string; body?: unknown }[] = [];
  const api: Api = async <T>(path: string, method?: string, body?: unknown) => {
    calls.push({ path, method, body });
    const r = responses[path];
    if (r instanceof Error) throw r;
    return r as T;
  };
  return { api, calls };
}

test("load кладёт ответ сервера в состояние и уведомляет подписчиков", async () => {
  const { api } = fakeApi({ "/api/notifications": state(["a", "b"], 2) });
  const m = createNotificationsModel(api);
  const seen: number[] = [];
  m.subscribe((s) => seen.push(s.unread));
  await m.load();
  assert.equal(m.get().items.length, 2);
  assert.deepEqual(seen, [0, 2]); // первое значение — сразу при подписке
});

test("load при недоступном сервере молча оставляет состояние", async () => {
  const { api } = fakeApi({ "/api/notifications": new Error("down") });
  const m = createNotificationsModel(api);
  await m.load();
  assert.equal(m.get().items.length, 0);
});

test("markRead / dismiss / check ходят в нужные ручки POST и обновляют состояние", async () => {
  const { api, calls } = fakeApi({
    "/api/notifications/read": state(["a"], 0),
    "/api/notifications/dismiss": state([], 0),
    "/api/notifications/check": state(["x"], 1),
  });
  const m = createNotificationsModel(api);
  await m.markRead("a");
  await m.dismiss("");
  await m.check();
  assert.deepEqual(calls.map((c) => [c.path, c.method, c.body]), [
    ["/api/notifications/read", "POST", { key: "a" }],
    ["/api/notifications/dismiss", "POST", { key: "" }],
    ["/api/notifications/check", "POST", {}],
  ]);
  assert.equal(m.get().items[0].key, "x");
});

test("ошибка check пробрасывается (в UI — тост), состояние не портится", async () => {
  const { api } = fakeApi({ "/api/notifications/check": new Error("checked a moment ago") });
  const m = createNotificationsModel(api);
  await assert.rejects(m.check(), /moment ago/);
  assert.equal(m.get().items.length, 0);
});
