// node --experimental-strip-types --test web/client/shared/proxybase.test.ts
import assert from "node:assert/strict";
import { test } from "node:test";
import { proxyBase } from "./proxybase.ts";

const doc = (content: string | null) => ({
  querySelector: () => (content === null ? null : { getAttribute: () => content }),
}) as unknown as Pick<Document, "querySelector">;
const loc = (hostname: string) => ({ protocol: "http:", hostname });

test("порт прокси из meta → абсолютный адрес на том же хосте", () => {
  assert.equal(proxyBase(doc("8001"), loc("127.0.0.1")), "http://127.0.0.1:8001");
  assert.equal(proxyBase(doc("8001"), loc("192.168.1.5")), "http://192.168.1.5:8001"); // удалённое устройство
  assert.equal(proxyBase(doc("8001"), loc("[::1]")), "http://[::1]:8001");
});

test("sad path: нет meta или значение некорректно → тот же origin", () => {
  for (const c of [null, "", "abc", "0", "70000", "80.5", "-1"]) assert.equal(proxyBase(doc(c), loc("127.0.0.1")), "", String(c));
});
