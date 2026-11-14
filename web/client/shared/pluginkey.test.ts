import { test } from "node:test";
import assert from "node:assert/strict";
import { pluginFrameUrl, pluginKey, sourcePluginKey } from "./pluginkey.ts";

test("pluginKey: обычный плагин — id, виджет автономного — id/виджет", () => {
  assert.equal(pluginKey({ id: "twitch-chat" }), "twitch-chat");
  assert.equal(pluginKey({ id: "music", widget: "queue" }), "music/queue");
  assert.notEqual(pluginKey({ id: "music", widget: "a" }), pluginKey({ id: "music", widget: "b" }));
  assert.equal(sourcePluginKey({ plugin: "music", widget: "queue" }), "music/queue");
  assert.equal(sourcePluginKey({}), "");
});

test("pluginFrameUrl: обычный плагин, виджет, настройки и экранирование", () => {
  assert.equal(pluginFrameUrl({ plugin: "demo" }), "/plugins/demo/");
  assert.equal(pluginFrameUrl({ plugin: "demo", settings: { channel: "a b", size: 20, on: true } }), "/plugins/demo/?channel=a+b&size=20&on=true");
  assert.equal(pluginFrameUrl({ plugin: "music", widget: "queue" }), "/plugins/music/widget/queue");
  assert.equal(pluginFrameUrl({ plugin: "music", widget: "queue", settings: { x: 1 } }), "/plugins/music/widget/queue?x=1");
  assert.equal(pluginFrameUrl({ plugin: "a/../b", widget: "x/y" }), "/plugins/a%2F..%2Fb/widget/x%2Fy");
  assert.equal(pluginFrameUrl({}), null);
  assert.equal(pluginFrameUrl({ widget: "queue" }), null);
});
