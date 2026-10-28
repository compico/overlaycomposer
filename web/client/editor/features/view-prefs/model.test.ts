// node --experimental-strip-types --test web/client/editor/features/view-prefs/model.test.ts
import assert from "node:assert/strict";
import { test } from "node:test";
import { createViewPrefsModel, type PrefStorage } from "./model.ts";

const mem = (init: Record<string, string> = {}): PrefStorage & { data: Record<string, string> } => {
  const data = { ...init };
  return { data, get: (k) => data[k] ?? null, set: (k, v) => { data[k] = v; } };
};

test("умолчания: компоновка выкл., прилипание вкл.", () => {
  const m = createViewPrefsModel(mem());
  assert.deepEqual(m.get(), { compose: false, snap: true });
});

test("значения читаются из хранилища и записываются при переключении", () => {
  const st = mem({ "oc.compose": "1", "oc.snap": "0" });
  const m = createViewPrefsModel(st);
  assert.deepEqual(m.get(), { compose: true, snap: false });
  m.toggleCompose();
  m.toggleSnap();
  assert.deepEqual(m.get(), { compose: false, snap: true });
  assert.deepEqual(st.data, { "oc.compose": "0", "oc.snap": "1" });
});

test("подписчик получает каждое изменение", () => {
  const m = createViewPrefsModel(mem());
  const seen: boolean[] = [];
  m.subscribe((s) => seen.push(s.compose));
  m.toggleCompose();
  m.toggleCompose();
  assert.deepEqual(seen, [false, true, false]);
});

test("sad path: хранилище бросает исключения — работаем с умолчаниями и не падаем", () => {
  const broken: PrefStorage = { get: () => { throw new Error("denied"); }, set: () => { throw new Error("denied"); } };
  const m = createViewPrefsModel(broken);
  assert.deepEqual(m.get(), { compose: false, snap: true });
  m.toggleSnap();
  assert.equal(m.get().snap, false);
});
