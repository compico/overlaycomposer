// node --experimental-strip-types --test web/client/editor/features/snapshots/model.test.ts
import assert from "node:assert/strict";
import { test } from "node:test";
import { fakeApi } from "../../lib/fakeapi.test-helper.ts";
import { createSnapshotsModel, kindLabel, parseLimit, snapshotLabel } from "./model.ts";

test("parseLimit: целое 1…200", () => {
  assert.equal(parseLimit("30"), 30);
  assert.equal(parseLimit(" 1 "), 1);
  assert.equal(parseLimit("200"), 200);
  for (const bad of ["", "0", "201", "-3", "2.5", "abc"]) assert.equal(parseLimit(bad), null, bad);
});

test("kindLabel и snapshotLabel", () => {
  assert.equal(kindLabel("auto"), "on close");
  assert.equal(kindLabel("import"), "before import");
  assert.match(snapshotLabel({ name: "x", kind: "manual", size: 2048, modTime: "2026-11-21T19:00:00Z" }, (n) => `${n}B`), /— manual, 2048B$/);
  assert.equal(snapshotLabel({ name: "x.json", kind: "auto", size: 1, modTime: "bad" }, (n) => `${n}B`), "x.json — on close, 1B");
});

test("модель: запросы и тела", async () => {
  const info = { auto: true, max: 30, files: [] };
  const { api, calls, bodies } = fakeApi({
    "GET /api/settings/backup": () => info,
    "PUT /api/settings/backup": () => ({ ...info, max: 10 }),
    "POST /api/settings/backup/snapshot": () => info,
    "GET /api/settings/backup/files/oc-settings-1.json": () => ({ name: "oc-settings-1.json", text: '{"format":1}' }),
    "DELETE /api/settings/backup/files/oc-settings-1.json": () => info,
  });
  const m = createSnapshotsModel(api);
  assert.equal((await m.info()).max, 30);
  assert.equal((await m.save({ max: 10 })).max, 10);
  assert.deepEqual(bodies[1], { max: 10 });
  await m.snapshot();
  assert.equal(await m.read("oc-settings-1.json"), '{"format":1}');
  await m.remove("oc-settings-1.json");
  assert.equal(calls.length, 5);
});
