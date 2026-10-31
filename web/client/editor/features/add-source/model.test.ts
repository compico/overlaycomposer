// node --experimental-strip-types --test web/client/editor/features/add-source/model.test.ts
import assert from "node:assert/strict";
import { test } from "node:test";
import type { PluginManifest, Source } from "../../../../shared/types.ts";
import { fakeApi } from "../../lib/fakeapi.test-helper.ts";
import { createAddSourceModel, existingMeta, maskUrl, pickSelection, pluginHint, uniqueName, urlView } from "./model.ts";

test("maskUrl прячет всё после ? и #", () => {
  assert.equal(maskUrl("http://a/b"), "http://a/b");
  assert.equal(maskUrl("http://a/b?token=1"), "http://a/b?••••••••");
  assert.equal(maskUrl("http://a/b#x"), "http://a/b#••••••••");
});

test("maskUrl прячет токены в пути, но не обычные слова и slug'и", () => {
  const M = "••••••••";
  assert.equal(maskUrl("https://streamelements.com/overlay/5f1c2a3b4d5e6f7a8b9c0d1e/eyJhbGciOiJIUzI1NiJ9abc123"), `https://streamelements.com/overlay/${M}/${M}`);
  assert.equal(maskUrl("https://x.example/widget/123e4567-e89b-12d3-a456-426614174000"), `https://x.example/widget/${M}`);
  assert.equal(maskUrl("https://x.example/widget/aB3dE5gH7jK9mN1pQ3sT5v?x=1"), `https://x.example/widget/${M}?${M}`);
  // не секреты
  assert.equal(maskUrl("https://x.example/overlay/alerts/my-alerts-widget"), "https://x.example/overlay/alerts/my-alerts-widget");
  assert.equal(maskUrl("https://x.example/some-long-readable-slug-with-words-2024"), "https://x.example/some-long-readable-slug-with-words-2024");
  assert.equal(maskUrl("https://x.example/internationalization"), "https://x.example/internationalization"); // длинное слово без цифр
  assert.equal(maskUrl("https://x.example/v2/overlay"), "https://x.example/v2/overlay");
  // порт и относительные адреса
  assert.equal(maskUrl("http://127.0.0.1:8000/a/aB3dE5gH7jK9mN1pQ3sT5v"), `http://127.0.0.1:8000/a/${M}`);
  assert.equal(maskUrl("/plugins/chat/"), "/plugins/chat/");
  assert.equal(maskUrl(""), "");
});

test("urlView: скрыт, пока не показан; без секретов кнопка не нужна", () => {
  const u = "https://a.example/w?token=abc";
  assert.deepEqual(urlView(u, false), { text: "https://a.example/w?••••••••", readOnly: true, canToggle: true });
  assert.deepEqual(urlView(u, true), { text: u, readOnly: false, canToggle: true });
  assert.deepEqual(urlView("https://a.example/w", false), { text: "https://a.example/w", readOnly: false, canToggle: false });
  assert.deepEqual(urlView("", false), { text: "", readOnly: false, canToggle: false });
});

test("uniqueName и pickSelection", () => {
  assert.equal(uniqueName("Chat", ["Other"]), "Chat");
  assert.equal(uniqueName("Chat", ["Chat", "Chat 2"]), "Chat 3");
  assert.equal(pickSelection(["a", "b"], "b"), "b");
  assert.equal(pickSelection(["a", "b"], "gone"), "a");
  assert.equal(pickSelection([], "x"), "");
});

test("existingMeta и pluginHint", () => {
  assert.equal(existingMeta({ type: "plugin", data: { plugin: "osu" } } as unknown as Source), "plugin · osu");
  assert.equal(existingMeta({ type: "iframe", data: { url: "http://a?t=1" } } as unknown as Source), "http://a?••••••••");
  assert.equal(pluginHint(undefined), "");
  assert.equal(pluginHint({} as PluginManifest), "Asks for no permissions.");
  assert.match(pluginHint({ permissions: ["net.local"] } as PluginManifest), /^Asks for: /);
});

function setup(plugins: PluginManifest[] = [], sources: Source[] = [], confirm = true) {
  const added: unknown[][] = [];
  let loads = 0;
  const f = fakeApi({ "POST /api/sources": () => ({}) });
  const model = createAddSourceModel({
    api: f.api,
    editor: {
      get: () => ({ sources, plugins }),
      loadSources: async () => { loads++; },
      loadPlugins: async () => {},
      addObject: (...a) => { added.push(a); return null; },
    },
    confirmPermissions: () => confirm,
  });
  return { model, f, added, loads: () => loads };
}

test("createIframe: POST, перечитывает sources, ставит слой с минимумом размера", async () => {
  const { model, f, added, loads } = setup();
  assert.equal(await model.createIframe({ name: " Chat ", url: " http://x ", proxy: true, w: 5, h: 0 }), true);
  assert.deepEqual(f.bodies[0] && { ...(f.bodies[0] as object), id: 0 }, { id: 0, name: "Chat", type: "iframe", data: { url: "http://x", proxy: true } });
  assert.equal(loads(), 1);
  assert.deepEqual(added[0].slice(1), [20, 300]);
});

test("createIframe, sad path: без имени или адреса ничего не отправляется", async () => {
  const { model, f, added } = setup();
  assert.equal(await model.createIframe({ name: "", url: "http://x", proxy: false, w: 1, h: 1 }), false);
  assert.equal(await model.createIframe({ name: "A", url: "  ", proxy: false, w: 1, h: 1 }), false);
  assert.deepEqual(f.calls, []);
  assert.equal(added.length, 0);
});

test("usePlugin: имя уникальное, разрешения — по ответу пользователя", async () => {
  const p = { id: "p", name: "Now", permissions: ["net.local"], defaultSize: { width: 300, height: 100 } } as unknown as PluginManifest;
  const yes = setup([p], [{ name: "Now" } as Source], true);
  assert.equal(await yes.model.usePlugin("p"), true);
  const b = yes.f.bodies[0] as { name: string; data: { granted: string[] } };
  assert.equal(b.name, "Now 2");
  assert.deepEqual(b.data.granted, ["net.local"]);
  assert.deepEqual(yes.added[0].slice(1), [300, 100]);

  const no = setup([p], [], false);
  await no.model.usePlugin("p");
  assert.deepEqual((no.f.bodies[0] as { data: { granted: string[] } }).data.granted, []);
});

test("usePlugin, sad path: неизвестный плагин; useExisting с пустым id", async () => {
  const { model, f, added } = setup();
  assert.equal(await model.usePlugin("nope"), false);
  model.useExisting("");
  assert.deepEqual(f.calls, []);
  assert.equal(added.length, 0);
  model.useExisting("s1");
  assert.equal(added.length, 1);
});
