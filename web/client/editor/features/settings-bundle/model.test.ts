// node --experimental-strip-types --test web/client/editor/features/settings-bundle/model.test.ts
import assert from "node:assert/strict";
import { test } from "node:test";
import { fakeApi } from "../../lib/fakeapi.test-helper.ts";
import { RawJson } from "../../lib/api.ts";
import { createBundleModel, defaultSelection, reportLines, selectable, selectedCount, type SectionPlan } from "./model.ts";

const plan: SectionPlan[] = [
  { name: "layouts", known: true, present: true, items: [
    { id: "a", status: "new" }, { id: "b", status: "replace" }, { id: "c", status: "same" }, { id: "d", status: "problem", reason: "x" },
  ] },
  { name: "store", known: true, present: true, items: [{ id: "o/n", status: "new", optional: true }] },
  { name: "future", known: false, present: true },
];

test("defaultSelection: применимые и не необязательные", () => {
  const sel = defaultSelection(plan);
  assert.deepEqual(sel, { layouts: { a: "replace", b: "replace" } });
  assert.equal(selectedCount(sel), 2);
  assert.ok(selectable({ id: "x", status: "replace" }) && !selectable({ id: "x", status: "same" }) && !selectable({ id: "x", status: "problem" }));
});

test("exportUrl, plan и apply: тело уходит как есть, без пересборки", async () => {
  const text = '{"format":1,"sections":{"layouts":{"10":1,"2":2}},"checksums":{}}';
  const { api, calls, bodies } = fakeApi({
    "POST /api/settings/import/plan": () => ({ format: 1, createdAt: "t", sections: plan }),
    "POST /api/settings/import/apply": () => ({ reports: [{ name: "layouts", outcomes: [{ id: "a", ok: true }] }] }),
  });
  const m = createBundleModel(api);
  assert.equal(m.exportUrl(["layouts", "deck"]), "/api/settings/export?download=1&sections=layouts%2Cdeck");
  assert.equal((await m.plan(text)).sections.length, 3);
  const reports = await m.apply(text, { layouts: { a: "replace" } });
  assert.equal(reports[0].name, "layouts");
  assert.deepEqual(calls, ["POST /api/settings/import/plan", "POST /api/settings/import/apply"]);
  assert.ok(bodies[0] instanceof RawJson && (bodies[0] as RawJson).text === text);
  assert.equal((bodies[1] as RawJson).text, `{"file":${text},"selection":{"layouts":{"a":"replace"}}}`);
  assert.doesNotThrow(() => JSON.parse((bodies[1] as RawJson).text));
});

test("reportLines", () => {
  assert.deepEqual(reportLines([
    { name: "layouts", outcomes: [{ id: "a", ok: true, note: "1 object skipped" }, { id: "b", ok: false, error: "boom" }] },
    { name: "deck", error: "unreadable" },
  ]), ["Layouts · a: done (1 object skipped)", "Layouts · b: failed — boom", "Deck panel: unreadable"]);
});
