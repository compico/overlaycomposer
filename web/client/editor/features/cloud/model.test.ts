// node --experimental-strip-types --test web/client/editor/features/cloud/model.test.ts
import assert from "node:assert/strict";
import { test } from "node:test";
import { fakeApi } from "../../lib/fakeapi.test-helper.ts";
import { createCloudModel, fileLabel, formatSize } from "./model.ts";

test("formatSize и fileLabel", () => {
  assert.equal(formatSize(12), "12 B");
  assert.equal(formatSize(2048), "2.0 KB");
  assert.equal(formatSize(3 * 1024 * 1024), "3.0 MB");
  assert.match(fileLabel({ id: "a", name: "oc-settings-a.json", size: 2048, modTime: "2026-11-21T08:30:00Z", fingerprint: "f" }), /^oc-settings-a\.json — 2\.0 KB, /);
  assert.equal(fileLabel({ id: "a", name: "a", size: 1, modTime: "bad", fingerprint: "f" }), "a — 1 B");
});

test("модель: запросы и тела", async () => {
  const { api, calls, bodies } = fakeApi({
    "GET /api/cloud": () => ({ provider: "folder", folder: "/x", connected: true }),
    "PUT /api/cloud": () => ({ provider: "folder", folder: "/y", connected: true }),
    "DELETE /api/cloud": () => ({ provider: "", connected: false }),
    "GET /api/cloud/files": () => ({ files: [{ id: "oc-settings-a.json", name: "oc-settings-a.json", size: 1, modTime: "", fingerprint: "f" }] }),
    "POST /api/cloud/upload": () => ({ id: "n", name: "n", size: 1, modTime: "", fingerprint: "g" }),
    "GET /api/cloud/files/oc-settings-a.json": () => ({ id: "oc-settings-a.json", text: '{"format":1}' }),
    "DELETE /api/cloud/files/oc-settings-a.json": () => ({ ok: true }),
  });
  const m = createCloudModel(api);
  assert.equal((await m.status()).connected, true);
  assert.equal((await m.connect("/y")).folder, "/y");
  assert.deepEqual(bodies[1], { folder: "/y" });
  assert.equal((await m.list()).length, 1);
  await m.upload({ sections: ["layouts"] });
  assert.deepEqual(bodies[3], { sections: ["layouts"] });
  await m.upload({ sections: ["layouts"], name: "oc-settings-a.json", expect: "f" });
  assert.deepEqual(bodies[4], { sections: ["layouts"], name: "oc-settings-a.json", expect: "f" });
  assert.equal(await m.download("oc-settings-a.json"), '{"format":1}');
  await m.remove("oc-settings-a.json");
  assert.equal((await m.disconnect()).connected, false);
  assert.equal(calls.length, 8);
});
