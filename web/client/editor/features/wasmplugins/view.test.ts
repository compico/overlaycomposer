import { test } from "node:test";
import assert from "node:assert/strict";
import { settingsUrl } from "./view.ts";

test("settingsUrl: путь в статике плагина, сегменты кодируются", () => {
  assert.equal(settingsUrl("music", "settings.html"), "/plugins/music/ui/settings.html");
  assert.equal(settingsUrl("music", "pages/a b.html"), "/plugins/music/ui/pages/a%20b.html");
  assert.equal(settingsUrl("a/b", "x.html"), "/plugins/a%2Fb/ui/x.html");
});
