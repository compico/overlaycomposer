// node --experimental-strip-types --test web/client/editor/features/store/wasm.test.ts
import assert from "node:assert/strict";
import { test } from "node:test";
import type { Api } from "../../lib/api.ts";
import { createWasmStoreModel, type WasmSource } from "./wasm.ts";

const sources = (n: number): WasmSource[] => [{ repo: "o/r", plugins: n ? [{ id: "music", name: "Music", version: "1.0.0", repo: "o/r", tag: "music-v1.0.0", versions: [], installed: false, updateAvailable: false }] : [] }];

function setup(handlers: Record<string, (body?: unknown) => unknown>) {
  const calls: string[] = [];
  const api: Api = async <T>(path: string, method = "GET", body?: unknown) => {
    calls.push(`${method} ${path}`);
    const h = handlers[`${method} ${path}`];
    if (!h) throw new Error(`no handler ${method} ${path}`);
    return h(body) as T;
  };
  let changed = 0;
  return { model: createWasmStoreModel({ api, afterChange: async () => { changed++; } }), calls, changed: () => changed };
}

test("load кладёт источники; refresh добавляет ?refresh=1; при ошибке прежний каталог остаётся", async () => {
  let fail = false;
  const { model, calls } = setup({
    "GET /api/market/wasm": () => { if (fail) throw new Error("down"); return { sources: sources(1) }; },
    "GET /api/market/wasm?refresh=1": () => ({ sources: sources(0) }),
  });
  await model.load();
  assert.equal(model.get().sources[0].plugins.length, 1);
  fail = true;
  await model.load();
  assert.equal(model.get().error, "down");
  assert.equal(model.get().sources[0].plugins.length, 1);
  await model.load(true);
  assert.equal(model.get().error, null);
  assert.equal(model.get().sources[0].plugins.length, 0);
  assert.deepEqual(calls, ["GET /api/market/wasm", "GET /api/market/wasm", "GET /api/market/wasm?refresh=1"]);
});

test("install: отправляет тег и отпечаток, возвращает ответ сервера, перечитывает плагины и каталог", async () => {
  const { model, calls, changed } = setup({
    "POST /api/market/wasm/install": (b) => {
      assert.deepEqual(b, { repo: "o/r", id: "music", tag: "music-v1.0.0", sha256: "abc" });
      return { id: "music", version: "1.0.0", tag: "music-v1.0.0", sha256: "abc", previous: "0.9.0", addedPermissions: ["chat.send"] };
    },
    "GET /api/market/wasm": () => ({ sources: sources(1) }),
  });
  const res = await model.install({ repo: "o/r", id: "music", tag: "music-v1.0.0", sha256: "abc" });
  assert.deepEqual([res.previous, res.addedPermissions], ["0.9.0", ["chat.send"]]);
  assert.equal(changed(), 1);
  assert.deepEqual(calls, ["POST /api/market/wasm/install", "GET /api/market/wasm"]);
});

test("install без отпечатка не шлёт поле; ошибка сервера пробрасывается, каталог всё равно перечитывается", async () => {
  const { model, calls, changed } = setup({
    "POST /api/market/wasm/install": (b) => { assert.ok(!("sha256" in (b as object))); throw new Error("changed"); },
    "GET /api/market/wasm": () => ({ sources: sources(1) }),
  });
  await assert.rejects(model.install({ repo: "o/r", id: "music", tag: "music-v1.0.0" }), /changed/);
  assert.equal(changed(), 0);
  assert.deepEqual(calls, ["POST /api/market/wasm/install", "GET /api/market/wasm"]);
});

test("uninstall", async () => {
  const { model, calls, changed } = setup({
    "POST /api/market/wasm/uninstall": (b) => { assert.deepEqual(b, { id: "music" }); return { ok: true }; },
    "GET /api/market/wasm": () => ({ sources: sources(0) }),
  });
  await model.uninstall("music");
  assert.equal(changed(), 1);
  assert.deepEqual(calls, ["POST /api/market/wasm/uninstall", "GET /api/market/wasm"]);
});
