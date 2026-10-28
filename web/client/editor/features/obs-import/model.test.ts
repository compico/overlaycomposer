// node --experimental-strip-types --test web/client/editor/features/obs-import/model.test.ts
import assert from "node:assert/strict";
import { test } from "node:test";
import { fakeApi } from "../../lib/fakeapi.test-helper.ts";
import { createObsImportModel, reportLines, type ImportReport } from "./model.ts";

test("scenes и import ходят в нужные ручки", async () => {
  const report = { layout: { name: "L", objects: [{}, {}] }, newSources: [{}], reusedSources: 0, proxied: 1, skipped: [{ name: "x", reason: "unsupported" }], warnings: ["w"], bound: true } as unknown as ImportReport;
  const { api, calls, bodies } = fakeApi({ "GET /api/obs/scenes": () => ({ scenes: ["A", "B"] }), "POST /api/obs/import": () => report });
  const m = createObsImportModel(api);
  assert.deepEqual(await m.scenes(), ["A", "B"]);
  assert.equal((await m.import("A", true)).layout.name, "L");
  assert.deepEqual(calls, ["GET /api/obs/scenes", "POST /api/obs/import"]);
  assert.deepEqual(bodies[1], { scene: "A", bind: true });
  assert.deepEqual(reportLines(report), ["Imported “L”: 2 layer(s), 1 new source(s), 1 via proxy, bound to the scene", "skipped x: unsupported", "warning: w"]);
});
