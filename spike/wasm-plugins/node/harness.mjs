// Проверка ABI без Go-хоста: Node как «хост» для hello.wasm (WASI preview1 + oc.host_call).
// Запуск: node harness.mjs ../guest/hello.wasm
import { readFileSync } from "node:fs";
import { WASI } from "node:wasi";

const wasmPath = process.argv[2] ?? "../guest/hello.wasm";
const bytes = readFileSync(wasmPath);
const mod = await WebAssembly.compile(bytes);

const storage = new Map(); // состояние живёт в хосте и переживает перезапуск экземпляра
const logs = [], timers = [], fetches = [];

async function instantiate() {
  const wasi = new WASI({ version: "preview1", args: [], env: {} });
  let inst;
  const mem = () => new Uint8Array(inst.exports.memory.buffer);
  const read = (p, n) => Buffer.from(mem().subarray(p, p + n)).toString();
  const hostCall = (opP, opL, aP, aL) => {
    const op = read(opP, opL);
    const arg = aL ? JSON.parse(read(aP, aL)) : {};
    let out = null;
    switch (op) {
      case "log": logs.push(arg); break;
      case "storage.get": out = { value: storage.get(arg.key) ?? "" }; break;
      case "storage.set": storage.set(arg.key, arg.value); out = {}; break;
      case "timer.set": timers.push(arg); out = {}; break;
      case "http.fetch": fetches.push(arg); out = {}; break;
      default: out = { error: "unknown op " + op };
    }
    if (out === null) return 0n;
    const data = Buffer.from(JSON.stringify(out));
    const p = inst.exports.alloc(data.length); // вложенный вызов экспорта плагина из host_call
    mem().set(data, p);
    return (BigInt(p) << 32n) | BigInt(data.length);
  };
  inst = await WebAssembly.instantiate(mod, { wasi_snapshot_preview1: wasi.wasiImport, oc: { host_call: hostCall } });
  wasi.initialize(inst);
  return inst;
}

function call(inst, name, arg) {
  const e = inst.exports;
  const put = (s) => { const b = Buffer.from(s); const p = e.alloc(b.length); new Uint8Array(e.memory.buffer).set(b, p); return [p, b.length]; };
  const [np, nl] = put(name);
  const [ap, al] = put(arg === undefined ? "" : JSON.stringify(arg));
  const r = e.oc_call(np, nl, ap, al);
  e.dealloc(np); e.dealloc(ap);
  if (r === 0n) return null;
  const p = Number(r >> 32n), n = Number(r & 0xffffffffn);
  const out = JSON.parse(Buffer.from(new Uint8Array(e.memory.buffer).subarray(p, p + n)).toString());
  e.dealloc(p);
  return out;
}

let ok = true;
const check = (name, cond, extra = "") => { if (!cond) ok = false; console.log((cond ? "PASS " : "FAIL ") + name, extra); };

let inst = await instantiate();
const e = inst.exports;
const initArg = Buffer.from(JSON.stringify({ apiVersion: 1, config: {} }));
const ip = e.alloc(initArg.length); new Uint8Array(e.memory.buffer).set(initArg, ip);
const r = e.oc_init(ip, initArg.length);
const meta = JSON.parse(Buffer.from(new Uint8Array(e.memory.buffer).subarray(Number(r >> 32n), Number((r >> 32n) + (r & 0xffffffffn)))).toString());
check("oc_init отдаёт метаданные и маршруты", meta.id === "hello" && meta.routes.length === 5 && meta.routes[0].path === "/greet", JSON.stringify(meta));

let g = call(inst, "greet", { method: "GET", path: "/greet", query: { name: "Ann" } });
check("greet: ответ и счётчик из storage хоста", g.status === 200 && g.body.hello === "Ann" && g.body.count === 1, JSON.stringify(g));
g = call(inst, "greet", { query: {} });
check("greet: второй вызов, имя по умолчанию", g.body.hello === "world" && g.body.count === 2, JSON.stringify(g));
check("плагин пишет в лог хоста с полями", logs.some((l) => l.msg === "greet" && l.fields.count === 2), JSON.stringify(logs.at(-1)));

const nf = call(inst, "nope", {});
check("sad path: неизвестный обработчик → 404 без падения", nf.status === 404, JSON.stringify(nf));

let trapped = null;
try { call(inst, "boom", {}); } catch (err) { trapped = err; }
check("sad path: паника плагина = trap, хост жив", trapped instanceof WebAssembly.RuntimeError, String(trapped));

inst = await instantiate(); // супервизор: перезапуск экземпляра
g = call(inst, "greet", { query: { name: "Bob" } });
check("после перезапуска состояние хоста сохранилось", g.status === 200 && g.body.count === 3, JSON.stringify(g));

// события хоста: таймер и асинхронный http
check("oc_init заказал таймер у хоста", timers.some((t) => t.id === "t" && t.repeat === true), JSON.stringify(timers));
const ev = (type, data) => call(inst, "on_event", { type, data });
ev("timer", { id: "t" }); ev("timer", { id: "t" });
check("событие timer: плагин считает тики", call(inst, "ticks", {}).body.ticks === 2);
g = call(inst, "fetch", { query: { url: "https://example.test/x" } });
check("fetch: обработчик отвечает сразу (202), запрос уходит хосту", g.status === 202 && fetches.length === 1 && fetches[0].url === "https://example.test/x", JSON.stringify([g, fetches]));
check("fetchResult до ответа — пусто", call(inst, "fetchResult", {}).body.result === null);
ev("http.response", { id: "last", status: 200, body: "ok" });
check("событие http.response: результат доступен", call(inst, "fetchResult", {}).body.result?.body === "ok");
check("sad: мусорное событие не роняет плагин", (() => { try { call(inst, "on_event", "не json"); ev("unknown", {}); return true; } catch { return false; } })());

console.log(ok ? "ALL OK" : "FAILED", `(wasm ${(bytes.length / 1048576).toFixed(1)} MB)`);
process.exit(ok ? 0 : 1);
