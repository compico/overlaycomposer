// node --experimental-strip-types --test web/client/editor/lib/store.test.ts
import assert from "node:assert/strict";
import { test } from "node:test";
import { createStore } from "./store.ts";

test("subscribe вызывает сразу и при каждом изменении; отписка работает", () => {
  const s = createStore(1);
  const seen: number[] = [];
  const off = s.subscribe((v) => seen.push(v));
  s.set(2);
  s.update((v) => v + 1);
  off();
  s.set(10);
  assert.deepEqual(seen, [1, 2, 3]);
  assert.equal(s.get(), 10);
});

test("то же значение подписчиков не будит", () => {
  const s = createStore("a");
  let n = 0;
  s.subscribe(() => n++);
  s.set("a");
  assert.equal(n, 1);
});
