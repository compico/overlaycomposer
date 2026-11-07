import { test } from "node:test";
import assert from "node:assert/strict";
import type { Api } from "../../lib/api.ts";
import {
  buildConfig, configFields, createWasmModel, formValues, missingGrants, needsAttention, permissionText,
  sortRewards, toggleGrant, twitchLine, validateNewReward, type Reward, type WasmInfo, type WasmPlugin,
} from "./model.ts";

const plugin = (o: Partial<WasmPlugin> = {}): WasmPlugin => ({
  meta: { id: "music", name: "Music", version: "0.1.0", permissions: ["storage", "twitch.redemption"] },
  file: "music.wasm", state: "running", restarts: 0, granted: [], ...o,
});
const info = (plugins: WasmPlugin[]): WasmInfo => ({ plugins, problems: [], dirs: ["/x/ocplugs"], twitch: { state: "off" } });

function fakeApi(handler: (path: string, method: string, body: unknown) => unknown) {
  const calls: { path: string; method: string; body: unknown }[] = [];
  const api: Api = async <T,>(path: string, method = "GET", body?: unknown) => {
    calls.push({ path, method, body });
    const r = handler(path, method, body);
    if (r instanceof Error) throw r;
    return r as T;
  };
  return { api, calls };
}

test("load: успех кладёт список, отказ сервера скрывает раздел", async () => {
  const ok = fakeApi(() => ({ plugins: [plugin()], problems: null, dirs: null, twitch: { state: "off" } }));
  const m = createWasmModel(ok.api);
  await m.load();
  assert.equal(m.get()?.plugins.length, 1);
  assert.deepEqual(m.get()?.problems, []); // null от сервера не ломает представление
  assert.deepEqual(m.get()?.dirs, []);

  const denied = fakeApi(() => new Error("403"));
  const m2 = createWasmModel(denied.api);
  await m2.load();
  assert.equal(m2.get(), null);
});

test("setGrants/setConfig/restart: правильные запросы, id экранируется, состояние обновляется из ответа", async () => {
  const { api, calls } = fakeApi(() => info([plugin({ granted: ["storage"] })]));
  const m = createWasmModel(api);
  await m.setGrants("a/b", ["storage"]);
  await m.setConfig("music", { rewardId: "r1" });
  await m.restart("music");
  assert.deepEqual(calls[0], { path: "/api/wasmplugins/a%2Fb/grants", method: "PUT", body: { granted: ["storage"] } });
  assert.deepEqual(calls[1], { path: "/api/wasmplugins/music/config", method: "PUT", body: { rewardId: "r1" } });
  assert.deepEqual(calls[2], { path: "/api/wasmplugins/music/restart", method: "POST", body: {} });
  assert.deepEqual(m.get()?.plugins[0].granted, ["storage"]);
});

test("ошибка сервера при сохранении пробрасывается, прежний список остаётся", async () => {
  let fail = false;
  const { api } = fakeApi(() => (fail ? new Error("bad config") : info([plugin()])));
  const m = createWasmModel(api);
  await m.load();
  fail = true;
  await assert.rejects(m.setConfig("music", {}), /bad config/);
  assert.equal(m.get()?.plugins.length, 1);
});

test("rewards: null от сервера — пустой список, управляемые награды первыми", async () => {
  const none = createWasmModel(fakeApi(() => ({ rewards: null })).api);
  assert.deepEqual(await none.rewards(), []);
  const r = (id: string, title: string, manageable: boolean): Reward => ({ id, title, cost: 1, prompt: "", inputRequired: false, enabled: true, manageable });
  const list = sortRewards([r("1", "B", false), r("2", "Z", true), r("3", "A", true)]);
  assert.deepEqual(list.map((x) => x.id), ["3", "2", "1"]);
});

test("createReward отправляет поля как есть", async () => {
  const { api, calls } = fakeApi(() => ({ id: "new" }));
  const m = createWasmModel(api);
  const r = await createWasmModel(api).createReward({ title: "T", cost: 5, prompt: "p", inputRequired: true });
  assert.equal(r.id, "new");
  assert.equal(calls[0].method, "POST");
  assert.deepEqual(calls[0].body, { title: "T", cost: 5, prompt: "p", inputRequired: true });
  void m;
});

test("права: недостающие, внимание, переключение сохраняет порядок и отбрасывает чужие", () => {
  const p = plugin({ granted: ["storage"] });
  assert.deepEqual(missingGrants(p), ["twitch.redemption"]);
  assert.equal(needsAttention(p), true);
  assert.equal(needsAttention(plugin({ granted: ["storage", "twitch.redemption"] })), false);
  assert.equal(needsAttention(plugin({ granted: ["storage", "twitch.redemption"], state: "failed" })), true);
  assert.deepEqual(toggleGrant(p, "twitch.redemption", true), ["storage", "twitch.redemption"]);
  assert.deepEqual(toggleGrant(plugin({ granted: ["timer", "storage"] }), "storage", false), []); // timer плагин не запрашивал
  assert.match(permissionText("storage"), /data/);
  assert.equal(permissionText("weird"), "weird");
});

test("configFields: поддерживаемые типы, reward по format, прочие пропускаются", () => {
  const f = configFields({
    rewardId: { type: "string", format: "twitch.reward", title: "Reward" },
    limit: { type: "integer" },
    ratio: { type: "number", description: "d" },
    on: { type: "boolean" },
    nested: { type: "object" },
    junk: 5,
  });
  assert.deepEqual(f.map((x) => [x.key, x.kind]), [["limit", "number"], ["on", "boolean"], ["ratio", "number"], ["rewardId", "reward"]]);
  assert.equal(f.find((x) => x.key === "rewardId")?.title, "Reward");
  assert.equal(f.find((x) => x.key === "limit")?.title, "limit");
  assert.deepEqual(configFields(undefined), []);
});

test("buildConfig: пустое пропускается, числа проверяются, булевы всегда пишутся", () => {
  const fields = configFields({ rewardId: { type: "string" }, limit: { type: "integer" }, on: { type: "boolean" } });
  assert.deepEqual(buildConfig(fields, { rewardId: " r1 ", limit: "", on: true }).config, { rewardId: "r1", on: true });
  assert.deepEqual(buildConfig(fields, { rewardId: "", limit: "7", on: false }).config, { limit: 7, on: false });
  assert.match(buildConfig(fields, { limit: "abc" }).error ?? "", /number/);
  assert.match(buildConfig(fields, { limit: "1.5" }).error ?? "", /whole/);
  assert.equal(buildConfig(fields, { limit: "Infinity" }).config, undefined);
});

test("formValues обратен buildConfig", () => {
  const fields = configFields({ rewardId: { type: "string" }, limit: { type: "integer" }, on: { type: "boolean" } });
  assert.deepEqual(formValues(fields, { rewardId: "r1", limit: 3 }), { limit: "3", on: false, rewardId: "r1" });
  assert.deepEqual(formValues(fields, undefined), { limit: "", on: false, rewardId: "" });
});

test("validateNewReward и twitchLine", () => {
  const ok = { title: "T", cost: 100, prompt: "", inputRequired: false };
  assert.equal(validateNewReward(ok), "");
  assert.match(validateNewReward({ ...ok, title: "  " }), /title/);
  assert.match(validateNewReward({ ...ok, cost: 0 }), /Cost/);
  assert.match(validateNewReward({ ...ok, cost: 1.5 }), /Cost/);
  assert.match(validateNewReward({ ...ok, cost: NaN }), /Cost/);
  assert.equal(twitchLine({ state: "connected" }, false), "Twitch: not needed by any plugin");
  assert.equal(twitchLine({ state: "error", detail: "no account" }, true), "Twitch: error — no account");
  assert.equal(twitchLine(undefined, true), "");
});
