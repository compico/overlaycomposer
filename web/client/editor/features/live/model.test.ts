// node --experimental-strip-types --test web/client/editor/features/live/model.test.ts
import assert from "node:assert/strict";
import { test } from "node:test";
import type { ObsInfo } from "../../../../shared/types.ts";
import { createLiveHandler, type LivePorts } from "./model.ts";

function setup(cur: { id: string; dirty: boolean } | null) {
  const log: string[] = [];
  const ports: LivePorts = {
    setActive: (id) => log.push(`active:${id}`),
    obs: (i) => log.push(`obs:${i.connected}`),
    scene: (n) => log.push(`scene:${n}`),
    loadLayouts: async () => { log.push("layouts"); },
    loadSources: async () => { log.push("sources"); },
    reloadCurrent: async () => { log.push("reload"); },
    current: () => cur,
  };
  return { log, handle: createLiveHandler(ports) };
}
const obs = { connected: true } as ObsInfo;

test("hello / obs:status / scene:changed", async () => {
  const { log, handle } = setup(null);
  await handle({ type: "hello", obs, layoutId: "L1" });
  await handle({ type: "obs:status", obs });
  await handle({ type: "scene:changed", scene: "Game", layoutId: null });
  assert.deepEqual(log, ["active:L1", "obs:true", "obs:true", "active:null", "scene:Game"]);
});

test("scene:changed без имени сцены не трогает бейдж", async () => {
  const { log, handle } = setup(null);
  await handle({ type: "scene:changed", scene: "", layoutId: "L2" });
  assert.deepEqual(log, ["active:L2"]);
});

test("layouts:changed и sources:changed перечитывают списки", async () => {
  const { log, handle } = setup(null);
  await handle({ type: "layouts:changed" });
  await handle({ type: "sources:changed" });
  assert.deepEqual(log, ["layouts", "sources"]);
});

test("layout:updated подтягивает чужую правку только без несохранённых", async () => {
  const clean = setup({ id: "A", dirty: false });
  await clean.handle({ type: "layout:updated", layoutId: "A" });
  assert.deepEqual(clean.log, ["reload"]);

  const dirty = setup({ id: "A", dirty: true });
  await dirty.handle({ type: "layout:updated", layoutId: "A" });
  assert.deepEqual(dirty.log, []);
});

test("layout:updated, sad path: другой layout или ничего не открыто", async () => {
  const other = setup({ id: "A", dirty: false });
  await other.handle({ type: "layout:updated", layoutId: "B" });
  const none = setup(null);
  await none.handle({ type: "layout:updated", layoutId: "A" });
  assert.deepEqual([...other.log, ...none.log], []);
});
