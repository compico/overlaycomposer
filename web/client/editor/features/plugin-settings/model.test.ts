// node --experimental-strip-types --test web/client/editor/features/plugin-settings/model.test.ts
import assert from "node:assert/strict";
import { test } from "node:test";
import type { Source } from "../../../../shared/types.ts";
import { fakeApi } from "../../lib/fakeapi.test-helper.ts";
import { createPluginSettingsModel, isHexColor, nextGranted, nextSettings, storageText } from "./model.ts";

test("nextSettings: добавляет, заменяет, удаляет и не мутирует исходный объект", () => {
  const cur = { a: 1, b: "x" };
  assert.deepEqual(nextSettings(cur, "c", true), { a: 1, b: "x", c: true });
  assert.deepEqual(nextSettings(cur, "a", 5), { a: 5, b: "x" });
  assert.deepEqual(nextSettings(cur, "a", undefined), { b: "x" });
  assert.deepEqual(cur, { a: 1, b: "x" });
  assert.deepEqual(nextSettings(undefined, "k", 1), { k: 1 });
});

test("nextGranted, storageText, isHexColor", () => {
  assert.deepEqual(nextGranted(["a"], "b", true), ["a", "b"]);
  assert.deepEqual(nextGranted(["a", "b"], "a", false), ["b"]);
  assert.deepEqual(nextGranted(undefined, "a", false), []);
  assert.equal(storageText({ keys: 3, bytes: 2048 }), "Plugin data: 3 keys, 2.0 KB");
  assert.equal(isHexColor("#a1B2c3"), true);
  assert.equal(isHexColor("red"), false);
  assert.equal(isHexColor(5), false);
});

test("saveSetting / setGranted шлют PUT и берут итог из ответа сервера", async () => {
  const f = fakeApi({ "PUT /api/sources/s%201": () => ({ data: { settings: { k: 2 }, granted: ["p"] } }) });
  const m = createPluginSettingsModel(f.api);
  const src = { id: "s 1", data: { settings: { k: 1 }, granted: [] } } as unknown as Source;
  await m.saveSetting(src, "k", 2);
  assert.deepEqual(f.bodies[0], { data: { settings: { k: 2 } } });
  assert.deepEqual(src.data.settings, { k: 2 });
  await m.setGranted(src, "p", true);
  assert.deepEqual(f.bodies[1], { data: { granted: ["p"] } });
  assert.deepEqual(src.data.granted, ["p"]);
});

test("sad path: ошибка сервера не меняет source", async () => {
  const f = fakeApi({ "PUT /api/sources/s": () => { throw new Error("boom"); } });
  const m = createPluginSettingsModel(f.api);
  const src = { id: "s", data: { settings: { k: 1 } } } as unknown as Source;
  await assert.rejects(m.saveSetting(src, "k", 2), /boom/);
  assert.deepEqual(src.data.settings, { k: 1 });
});

test("хранилище плагина: чтение и стирание", async () => {
  const f = fakeApi({ "GET /api/plugins/p/storage": () => ({ keys: 1, bytes: 10 }), "DELETE /api/plugins/p/storage": () => ({}) });
  const m = createPluginSettingsModel(f.api);
  assert.deepEqual(await m.storage("p"), { keys: 1, bytes: 10 });
  await m.eraseStorage("p");
  assert.deepEqual(f.calls, ["GET /api/plugins/p/storage", "DELETE /api/plugins/p/storage"]);
});
