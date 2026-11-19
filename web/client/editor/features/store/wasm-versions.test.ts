// node --experimental-strip-types --test web/client/editor/features/store/wasm-versions.test.ts
import assert from "node:assert/strict";
import { test } from "node:test";
import { compareVersions, installAction, versionChoices, type WasmVersion } from "./wasm-versions.ts";

const v = (version: string): WasmVersion => ({ version, tag: `p-v${version}` });

test("latest, диапазоны и точные версии", () => {
  const list = ["2.0.0", "1.5.1", "1.5.0", "1.4.0", "1.0.0"].map(v);
  const got = versionChoices(list).map((c) => `${c.value}=${c.version}`);
  // ^1.5 совпадает с ^1 (оба дают 1.5.1) — отдельный пункт не нужен; ^2 и ^1.4 не нужны: в них по одной версии
  assert.deepEqual(got, ["latest=2.0.0", "^1=1.5.1", "2.0.0=2.0.0", "1.5.1=1.5.1", "1.5.0=1.5.0", "1.4.0=1.4.0", "1.0.0=1.0.0"]);
});

test("минорная ветка не новейшая в мажоре — получает свой диапазон", () => {
  const list = ["1.6.0", "1.5.2", "1.5.1"].map(v);
  assert.deepEqual(versionChoices(list).map((c) => `${c.value}=${c.version}`), [
    "latest=1.6.0", "^1=1.6.0", "^1.5=1.5.2", "1.6.0=1.6.0", "1.5.2=1.5.2", "1.5.1=1.5.1",
  ]);
});

test("одна версия: latest и она же точная; чужие теги и пустой список", () => {
  assert.deepEqual(versionChoices([v("0.9.0")]).map((c) => c.value), ["latest", "0.9.0"]);
  assert.deepEqual(versionChoices([]), []);
  assert.deepEqual(versionChoices([{ version: "1.0.0-rc1", tag: "x" }]), []);
});

test("пункт хранит тег релиза и не больше 60 пунктов", () => {
  const c = versionChoices([v("1.2.3")]);
  assert.equal(c[0].tag, "p-v1.2.3");
  const many = Array.from({ length: 100 }, (_, i) => v(`1.0.${99 - i}`));
  assert.equal(versionChoices(many).length, 60);
});

test("compareVersions: числа, а не строки", () => {
  assert.ok(compareVersions("0.10.0", "0.9.0") > 0);
  assert.ok(compareVersions("1.0.0", "2.0.0") < 0);
  assert.equal(compareVersions("1.2.3", "1.2.3"), 0);
  assert.equal(compareVersions("x", "1.0.0"), 0);
});

test("installAction: установка, обновление, понижение, та же версия", () => {
  assert.deepEqual(installAction(undefined, "1.0.0"), { kind: "install", label: "Install 1.0.0" });
  assert.deepEqual(installAction("0.9.0", "0.10.0"), { kind: "update", label: "Update to 0.10.0" });
  assert.deepEqual(installAction("1.0.0", "0.8.1"), { kind: "downgrade", label: "Switch to 0.8.1" });
  assert.deepEqual(installAction("1.0.0", "1.0.0"), { kind: "same", label: "Installed" });
});
