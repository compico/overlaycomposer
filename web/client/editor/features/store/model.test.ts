// node --experimental-strip-types --test web/client/editor/features/store/model.test.ts
import assert from "node:assert/strict";
import { test } from "node:test";
import type { Api } from "../../lib/api.ts";
import { createStoreModel, type StoreData } from "./model.ts";

const data = (n: number): StoreData => ({ sources: [], installed: {}, hasToken: n > 0 });

function setup(handlers: Record<string, (body?: unknown) => unknown>) {
  const calls: string[] = [];
  const api: Api = async <T>(path: string, method = "GET", body?: unknown) => {
    calls.push(`${method} ${path}`);
    const h = handlers[`${method} ${path}`];
    if (!h) throw new Error(`no handler ${method} ${path}`);
    return h(body) as T;
  };
  let changed = 0;
  const model = createStoreModel({ api, afterChange: async () => { changed++; } });
  return { model, calls, changed: () => changed };
}

test("load кладёт каталог; refresh добавляет ?refresh=1", async () => {
  const { model, calls } = setup({ "GET /api/market": () => data(0), "GET /api/market?refresh=1": () => data(1) });
  await model.load();
  assert.equal(model.get().data?.hasToken, false);
  await model.load(true);
  assert.equal(model.get().data?.hasToken, true);
  assert.deepEqual(calls, ["GET /api/market", "GET /api/market?refresh=1"]);
});

test("ошибка загрузки: error заполнен, прежний каталог не теряется", async () => {
  let fail = false;
  const { model } = setup({ "GET /api/market": () => { if (fail) throw new Error("down"); return data(1); } });
  await model.load();
  fail = true;
  await model.load();
  assert.equal(model.get().error, "down");
  assert.equal(model.get().data?.hasToken, true);
  assert.equal(model.get().loading, false);
});

test("install: запрос, afterChange, затем перечитывание каталога", async () => {
  const { model, calls, changed } = setup({
    "POST /api/market/install": (b) => { assert.deepEqual(b, { repo: "o/r", id: "p", digest: "d" }); return {}; },
    "GET /api/market": () => data(0),
  });
  await model.install({ repo: "o/r", id: "p", digest: "d" });
  assert.deepEqual(calls, ["POST /api/market/install", "GET /api/market"]);
  assert.equal(changed(), 1);
});

test("упавшая операция всё равно перечитывает каталог и пробрасывает ошибку; afterChange не вызывается", async () => {
  const { model, calls, changed } = setup({
    "POST /api/market/install": () => { throw new Error("digest mismatch"); },
    "GET /api/market": () => data(0),
  });
  await assert.rejects(model.install({ repo: "o/r", id: "p", digest: "d" }), /digest mismatch/);
  assert.deepEqual(calls, ["POST /api/market/install", "GET /api/market"]);
  assert.equal(changed(), 0);
});

test("источники и токен: правильные ручки; токен обновляет каталог с refresh", async () => {
  const { model, calls } = setup({
    "POST /api/market/sources": () => ({}),
    "DELETE /api/market/sources?repo=o%2Fr": () => ({}),
    "PUT /api/market/token": () => ({}),
    "DELETE /api/market/token": () => ({}),
    "GET /api/market": () => data(0),
    "GET /api/market?refresh=1": () => data(1),
  });
  await model.addSource("o/r");
  await model.removeSource("o/r");
  await model.saveToken("t");
  await model.clearToken();
  assert.deepEqual(calls, [
    "POST /api/market/sources", "GET /api/market",
    "DELETE /api/market/sources?repo=o%2Fr", "GET /api/market",
    "PUT /api/market/token", "GET /api/market?refresh=1",
    "DELETE /api/market/token", "GET /api/market?refresh=1",
  ]);
});
