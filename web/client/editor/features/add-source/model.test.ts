// node --experimental-strip-types --test web/client/editor/features/add-source/model.test.ts
import assert from "node:assert/strict";
import { test } from "node:test";
import type { PluginManifest, Source } from "../../../../shared/types.ts";
import { fakeApi } from "../../lib/fakeapi.test-helper.ts";
import { createAddSourceModel, defaultWidgetName, existingMeta, stripCommonRoot, maskUrl, pickSelection, pluginHint, pluginTitle, uniqueName, urlView } from "./model.ts";

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

const pick = (path: string, data = "x") => ({ path, file: new Blob([data]) });

test("stripCommonRoot: папка, выбранная целиком, кладётся в корень виджета", () => {
  assert.deepEqual(stripCommonRoot([pick("w/index.html"), pick("w/css/a.css")]).map((f) => f.path), ["index.html", "css/a.css"]);
  // одиночные файлы и разные корни не трогаем
  assert.deepEqual(stripCommonRoot([pick("a.html"), pick("b.css")]).map((f) => f.path), ["a.html", "b.css"]);
  assert.deepEqual(stripCommonRoot([pick("a/x.html"), pick("b/y.css")]).map((f) => f.path), ["a/x.html", "b/y.css"]);
  assert.deepEqual(stripCommonRoot([]), []);
});

test("defaultWidgetName: имя файла без расширения; для index.html — папка", () => {
  assert.equal(defaultWidgetName([pick("alert.html"), pick("a.css")]), "alert");
  assert.equal(defaultWidgetName([pick("index.html"), pick("other.htm")]), "other");
  assert.equal(defaultWidgetName([pick("index.html")], "my-folder"), "my-folder");
  assert.equal(defaultWidgetName([pick("index.html")]), "index");
  assert.equal(defaultWidgetName([pick("css/a.css")]), "");
});

test("createLocalHtml: загружает файлы с путями, перечитывает плагины и ставит виджет", async () => {
  const p = { id: "local-alert", name: "Alert", defaultSize: { width: 640, height: 360 } } as unknown as PluginManifest;
  const added: unknown[][] = [];
  const g = fakeApi({ "POST /api/plugins/local": () => ({ id: "local-alert" }), "POST /api/sources": () => ({}) });
  const m2 = createAddSourceModel({
    api: g.api,
    editor: { get: () => ({ sources: [], plugins: [p] }), loadSources: async () => {}, loadPlugins: async () => {}, addObject: (...a) => { added.push(a); return null; } },
    confirmPermissions: () => false,
  });
  assert.equal(await m2.createLocalHtml({ name: " Alert ", files: [pick("alert.html"), pick("css/a.css")] }), true);
  const form = g.bodies[0] as FormData;
  assert.ok(form instanceof FormData);
  assert.equal(form.get("name"), "Alert");
  assert.deepEqual(form.getAll("paths"), ["alert.html", "css/a.css"]);
  assert.equal(form.getAll("files").length, 2);
  assert.deepEqual(g.calls, ["POST /api/plugins/local", "POST /api/sources"]);
  assert.deepEqual(added[0].slice(1), [640, 360]);
});

test("createLocalHtml, sad path: без имени или файлов ничего не отправляется", async () => {
  const { model, f } = setup();
  assert.equal(await model.createLocalHtml({ name: " ", files: [pick("a.html")] }), false);
  assert.equal(await model.createLocalHtml({ name: "A", files: [] }), false);
  assert.deepEqual(f.calls, []);
});

test("usePlugin: у автономного плагина несколько виджетов — выбирается именно тот, чей ключ передан", async () => {
  const a = { id: "music", widget: "queue", wasm: true, name: "Queue", defaultSize: { width: 400, height: 300 } } as unknown as PluginManifest;
  const b = { id: "music", widget: "now", wasm: true, name: "Now playing", defaultSize: { width: 500, height: 120 } } as unknown as PluginManifest;
  const { model, f, added } = setup([a, b], [], false);
  assert.equal(await model.usePlugin("music/now"), true);
  const body = f.bodies[0] as { name: string; data: { plugin: string; widget?: string; granted: string[] } };
  assert.equal(body.name, "Now playing");
  assert.equal(body.data.plugin, "music");
  assert.equal(body.data.widget, "now");
  assert.deepEqual(body.data.granted, []);
  assert.deepEqual(added[0].slice(1), [500, 120]);
  // ключ без виджета у такого плагина не находит ничего: «music» — не то же, что «music/now»
  assert.equal(await model.usePlugin("music"), false);
});

test("usePlugin: у обычного плагина в запросе нет поля widget", async () => {
  const p = { id: "p", name: "P", defaultSize: { width: 1, height: 1 } } as unknown as PluginManifest;
  const { model, f } = setup([p], [], false);
  await model.usePlugin("p");
  assert.equal("widget" in (f.bodies[0] as { data: object }).data, false);
});

test("pluginTitle/pluginHint/existingMeta различают автономные плагины", () => {
  assert.match(pluginTitle({ name: "Queue", version: "0.1.0", builtin: false, wasm: true }), /\(standalone\)/);
  assert.doesNotMatch(pluginTitle({ name: "X", builtin: true }), /standalone|custom/);
  assert.match(pluginHint({ wasm: true }), /standalone plugin/);
  assert.equal(existingMeta({ type: "plugin", data: { plugin: "music", widget: "queue" } }), "plugin · music/queue");
  assert.equal(existingMeta({ type: "plugin", data: { plugin: "demo" } }), "plugin · demo");
});
