// node --experimental-strip-types --test web/client/editor/features/store/deprecated.test.ts
import assert from "node:assert/strict";
import { test } from "node:test";
import { alternativeFor, deprecationFor, deprecationText, replacementUrl, type Deprecation } from "./deprecated.ts";
import type { StoreEntry, StoreSource } from "./model.ts";

const entry = (o: Partial<StoreEntry> = {}): StoreEntry => ({ id: "new", name: "New", repo: "a/b", installed: false, updateAvailable: false, digest: "d", ...o });
const dep = (o: Partial<Deprecation> = {}): Deprecation => ({ id: "old", kind: "widget", reason: "Replaced.", repo: "a/b", ...o });
const src = (o: Partial<StoreSource> = {}): StoreSource => ({ repo: "a/b", plugins: [], deprecated: [], ...o });

test("replacementUrl: https из записи, иначе страница репозитория, мусор — null", () => {
  assert.equal(replacementUrl({ id: "x", kind: "wasm", repo: "a/b", url: "https://x.test/r" }), "https://x.test/r");
  assert.equal(replacementUrl({ id: "x", kind: "wasm", repo: "a/b" }), "https://github.com/a/b/releases");
  assert.equal(replacementUrl({ id: "x", kind: "widget", repo: "a/b" }), "https://github.com/a/b");
  assert.equal(replacementUrl({ id: "x", kind: "wasm", repo: "a/b", url: "javascript:alert(1)" }), "https://github.com/a/b/releases");
  assert.equal(replacementUrl({ id: "x", kind: "wasm", repo: "no slash", url: "http://x.test" }), null);
  assert.equal(replacementUrl(undefined), null);
});

test("deprecationFor: источник установки в приоритете, wasm ищется во всех источниках", () => {
  const a = src({ repo: "a/b", deprecated: [dep({ reason: "from a" })] });
  const b = src({ repo: "c/d", deprecated: [dep({ reason: "from c" }), dep({ id: "w", kind: "wasm", reason: "wasm" })] });
  assert.equal(deprecationFor([a, b], "widget", "old", "c/d")?.reason, "from c");
  assert.equal(deprecationFor([a, b], "widget", "old", "C/D")?.reason, "from c");
  assert.equal(deprecationFor([a, b], "widget", "old")?.reason, "from a");
  assert.equal(deprecationFor([a, b], "wasm", "w")?.reason, "wasm");
  assert.equal(deprecationFor([a, b], "widget", "w"), null);
  assert.equal(deprecationFor([{ repo: "x/y", plugins: [] }], "widget", "old"), null); // старый сервер без поля deprecated
});

test("alternativeFor: виджет из каталога ставится, установленный — уже стоит, wasm — магазин автономных плагинов", () => {
  const d = dep({ replacedBy: { id: "new", kind: "widget", repo: "a/b" } });
  const fresh = alternativeFor(d, [src({ plugins: [entry()] })]);
  assert.deepEqual(fresh && { t: fresh.type, l: fresh.label }, { t: "install", l: "Install New" });
  const have = alternativeFor(d, [src({ plugins: [entry({ installed: true })] })]);
  assert.equal(have?.type, "installed");
  const upd = alternativeFor(d, [src({ plugins: [entry({ installed: true, updateAvailable: true })] })]);
  assert.equal(upd?.type === "install" && upd.label, "Update New");
  // замены нет в каталоге или она заблокирована — ссылка на репозиторий
  assert.deepEqual(alternativeFor(d, [src()]), { type: "link", label: "Get new", url: "https://github.com/a/b" });
  assert.equal(alternativeFor(d, [src({ plugins: [entry({ blocked: "foreign" })] })])?.type, "link");
  const w = dep({ replacedBy: { id: "tools", kind: "wasm", repo: "a/b", url: "https://github.com/a/b/releases?q=tools-v" } });
  assert.deepEqual(alternativeFor(w, [src()]), { type: "store", label: "Get tools in the plugin store", id: "tools" });
  assert.equal(alternativeFor(dep(), [src()]), null);
});

test("deprecationText", () => {
  assert.equal(deprecationText({ since: "2026-11-17", reason: "Use X." }), "Deprecated since 2026-11-17: Use X.");
  assert.equal(deprecationText({ reason: "Use X." }), "Deprecated: Use X.");
});
