// node --experimental-strip-types --test web/client/editor/features/store/wasm-lock.test.ts
import assert from "node:assert/strict";
import { test } from "node:test";
import { defaultSelection, parseLockFile, replacementText, statusLabel } from "./wasm-lock.ts";

const item = (o: Record<string, unknown> = {}): any => ({ id: "a", repo: "o/n", tag: "a-v1.0.0", version: "1.0.0", sha256: "x", status: "ok", installable: true, ...o });

test("parseLockFile: разбор и ошибки", () => {
  const f = parseLockFile(JSON.stringify({ version: 1, plugins: [{ id: "a", repo: "o/n", tag: "a-v1.0.0", version: "1.0.0", sha256: "ab", size: 5 }, 7, null] }));
  assert.equal(f.plugins.length, 1);
  assert.equal(f.plugins[0].size, 5);
  assert.throws(() => parseLockFile("{"), /not a JSON/);
  assert.throws(() => parseLockFile("{}"), /lock file/);
  assert.throws(() => parseLockFile("[]"), /lock file/);
});

test("defaultSelection: только устанавливаемые", () => {
  const s = defaultSelection([item({ id: "a" }), item({ id: "b", status: "removed", installable: false, problem: true }), item({ id: "c", status: "installed", installable: false })]);
  assert.deepEqual([...s], ["a"]);
});

test("statusLabel и replacementText", () => {
  assert.equal(statusLabel(item({ status: "differs", installedVersion: "2.0.0" })), "installed 2.0.0 — restore 1.0.0");
  assert.equal(statusLabel(item({ status: "removed" })), "removed");
  assert.equal(replacementText(item({ deprecation: { reason: "r", replacedBy: { id: "osu-tools", kind: "wasm", repo: "o/n" } } })), "Replacement: osu-tools (o/n)");
  assert.equal(replacementText(item({})), "");
});
