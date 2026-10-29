// node --experimental-strip-types --test web/client/editor/features/obs-status/model.test.ts
import assert from "node:assert/strict";
import { test } from "node:test";
import { obsBadge, sceneText } from "./model.ts";

test("подключён через overlay / через websocket", () => {
  assert.deepEqual(obsBadge({ connected: true, via: "overlay" }), { text: "OBS: connected (overlay)", ok: true, title: "Scene data comes from the overlay running in OBS" });
  const ws = obsBadge({ connected: true, via: "websocket" });
  assert.equal(ws.text, "OBS: connected (websocket)");
  assert.equal(ws.title, "Scene data comes from obs-websocket");
});

test("sad path: не подключён — причина из error или «Not connected»", () => {
  const withErr = obsBadge({ connected: false, via: null, error: "bad password" });
  assert.equal(withErr.ok, false);
  assert.match(withErr.title, /^bad password/);
  assert.match(obsBadge({ connected: false, via: null }).title, /^Not connected/);
});

test("sceneText", () => {
  assert.equal(sceneText("Gaming"), "scene: Gaming");
  assert.equal(sceneText(null), "");
  assert.equal(sceneText(""), "");
});
