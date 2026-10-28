// node --experimental-strip-types --test web/client/editor/features/settings.models.test.ts
// Модели раздела Settings: приложение, сервер/OBS, Twitch, устройства.
import assert from "node:assert/strict";
import { test } from "node:test";
import { fakeApi } from "../lib/fakeapi.test-helper.ts";
import { createAppModel } from "./app/model.ts";
import { createConfigModel } from "./config/model.ts";
import { createDevicesModel } from "./devices/model.ts";
import { createTwitchModel } from "./twitch/model.ts";

const app = { compiled: true, os: "linux", configFile: "c", dataDir: "d", logFile: "l", autostart: false, tray: true, trayAvailable: true, version: "v1" };

test("app: load, setAutostart перечитывает, недоступный сервер не ломает", async () => {
  let autostart = false;
  const { api, calls, bodies } = fakeApi({
    "GET /api/app": () => ({ ...app, autostart }),
    "PUT /api/app/autostart": (b) => { autostart = (b as { enabled: boolean }).enabled; return {}; },
  });
  const m = createAppModel(api);
  await m.load();
  assert.equal(m.get()?.autostart, false);
  await m.setAutostart(true);
  assert.equal(m.get()?.autostart, true);
  assert.deepEqual(calls, ["GET /api/app", "PUT /api/app/autostart", "GET /api/app"]);
  assert.deepEqual(bodies[1], { enabled: true });
  const down = createAppModel(fakeApi({}).api);
  await down.load();
  assert.equal(down.get(), null);
});

test("app: quit глотает обрыв соединения, open шлёт what", async () => {
  const { api, bodies } = fakeApi({ "POST /api/app/quit": () => { throw new Error("socket closed"); }, "POST /api/app/open": () => ({}) });
  const m = createAppModel(api);
  await m.quit();
  await m.open("logs");
  assert.deepEqual(bodies[1], { what: "logs" });
});

const cfg = { host: "127.0.0.1", port: 8000, debugProxy: false, obsEnabled: true, obsUrl: "ws://x", hasObsPassword: false, obs: { connected: false, via: null, permissionLevel: null, scene: null, scenes: [], error: null }, obsWsConnected: false, obsWsError: null, overlayUrl: "u", restartRequired: false };

test("config: load отдаёт значение и кладёт в состояние; refresh молчит при ошибке; save кладёт ответ", async () => {
  let fail = false;
  const { api, bodies } = fakeApi({
    "GET /api/config": () => { if (fail) throw new Error("restarting"); return cfg; },
    "PUT /api/config": () => ({ ...cfg, restartRequired: true }),
    "POST /api/obs/reconnect": () => ({}),
  });
  const m = createConfigModel(api);
  assert.equal((await m.load()).port, 8000);
  fail = true;
  await m.refresh(); // не бросает
  assert.equal(m.get()?.port, 8000);
  await assert.rejects(m.load(), /restarting/);
  const patch = { host: "0.0.0.0", port: 9000, debugProxy: true, obs: { enabled: false, url: "ws://y", password: "", clearPassword: false } };
  assert.equal((await m.save(patch)).restartRequired, true);
  assert.deepEqual(bodies.at(-1), patch);
  assert.equal(m.get()?.restartRequired, true);
  await m.reconnectObs();
});

test("twitch: 403/ошибка прячет раздел (null); saveApp и disconnect перечитывают; connectLink отдаёт ссылку", async () => {
  let allowed = true;
  const info = { clientId: "cid", hasSecret: true, redirectUri: "r", accounts: [] };
  const { api, calls, bodies } = fakeApi({
    "GET /api/twitch": () => { if (!allowed) throw new Error("forbidden"); return info; },
    "PUT /api/twitch/app": () => ({}),
    "POST /api/twitch/connect/bot": () => ({ url: "https://id.twitch.tv/x" }),
    "DELETE /api/twitch/accounts/streamer": () => ({}),
  });
  const m = createTwitchModel(api);
  await m.load();
  assert.equal(m.get()?.clientId, "cid");
  await m.saveApp("c2", "s2");
  assert.deepEqual(bodies[1], { clientId: "c2", clientSecret: "s2" });
  assert.equal(await m.connectLink("bot"), "https://id.twitch.tv/x");
  await m.disconnect("streamer");
  assert.equal(calls.filter((c) => c === "GET /api/twitch").length, 3);
  allowed = false;
  await m.load();
  assert.equal(m.get(), null);
});

test("devices: load, revoke кодирует id и перечитывает, ошибка → null", async () => {
  let ok = true;
  const info = { code: "123456", expiresIn: 10, devices: [], remoteEnabled: true, pairUrl: "p" };
  const { api, calls } = fakeApi({
    "GET /api/devices": () => { if (!ok) throw new Error("403"); return info; },
    "DELETE /api/devices/a%2Fb": () => ({}),
  });
  const m = createDevicesModel(api);
  await m.load();
  assert.equal(m.get()?.code, "123456");
  await m.revoke("a/b");
  assert.deepEqual(calls, ["GET /api/devices", "DELETE /api/devices/a%2Fb", "GET /api/devices"]);
  ok = false;
  await m.load();
  assert.equal(m.get(), null);
});
