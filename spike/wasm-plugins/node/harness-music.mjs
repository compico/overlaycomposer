// Проверка плагина «music» через Node-хост: события (погашения, таймер) и маршруты для аддона плеера.
// Запуск: node harness-music.mjs ../music/music.wasm
import { readFileSync } from "node:fs";
import { WASI } from "node:wasi";

const mod = await WebAssembly.compile(readFileSync(process.argv[2] ?? "../music/music.wasm"));
const storage = new Map();
const logs = [], updates = [], timers = [];

async function instantiate() {
  const wasi = new WASI({ version: "preview1", args: [], env: {} });
  let inst;
  const mem = () => new Uint8Array(inst.exports.memory.buffer);
  const read = (p, n) => Buffer.from(mem().subarray(p, p + n)).toString();
  const hostCall = (opP, opL, aP, aL) => {
    const op = read(opP, opL), arg = aL ? JSON.parse(read(aP, aL)) : {};
    let out = {};
    switch (op) {
      case "log": logs.push(arg); return 0n;
      case "storage.get": out = { value: storage.get(arg.key) ?? "" }; break;
      case "storage.set": storage.set(arg.key, arg.value); break;
      case "twitch.redemption.update": updates.push(`${arg.id}:${arg.status}`); break;
      case "timer.set": timers.push(arg); break;
      default: out = { error: "unknown op " + op };
    }
    const data = Buffer.from(JSON.stringify(out));
    const p = inst.exports.alloc(data.length);
    mem().set(data, p);
    return (BigInt(p) << 32n) | BigInt(data.length);
  };
  inst = await WebAssembly.instantiate(mod, { wasi_snapshot_preview1: wasi.wasiImport, oc: { host_call: hostCall } });
  wasi.initialize(inst);
  const e = inst.exports;
  const put = (s) => { const b = Buffer.from(s); const p = e.alloc(b.length); new Uint8Array(e.memory.buffer).set(b, p); return [p, b.length]; };
  const take = (r) => {
    if (r === 0n) return null;
    const p = Number(r >> 32n), n = Number(r & 0xffffffffn);
    const s = Buffer.from(new Uint8Array(e.memory.buffer).subarray(p, p + n)).toString();
    e.dealloc(p);
    return JSON.parse(s);
  };
  return {
    init(cfg) { const [p, n] = put(JSON.stringify({ apiVersion: 1, config: cfg })); const r = take(e.oc_init(p, n)); e.dealloc(p); return r; },
    call(name, arg) {
      const [np, nl] = put(name), [ap, al] = put(typeof arg === "string" ? arg : JSON.stringify(arg ?? {}));
      const r = take(e.oc_call(np, nl, ap, al)); e.dealloc(np); e.dealloc(ap); return r;
    },
    event(type, data) { return this.call("on_event", { type, data }); },
    route(handler, body) { return this.call(handler, { method: "GET", path: "/" + handler, query: {}, body: body ?? null }); },
  };
}

let ok = true;
const check = (n, c, x = "") => { if (!c) ok = false; console.log((c ? "PASS " : "FAIL ") + n, c ? "" : x); };
const red = (id, input, reward = "rw") => ({ id, rewardId: reward, userLogin: "viewer", input });
const link = (n) => `https://music.yandex.ru/album/${n}/track/${n * 10}`;

let p = await instantiate();
const meta = p.init({ rewardId: "rw" });
check("init: метаданные, маршруты, подписки", meta.id === "music" && meta.routes.length === 4 && meta.subscriptions.includes("twitch.redemption"), JSON.stringify(meta));
check("init: плагин сам просит периодический таймер", timers.some((t) => t.id === "tick" && t.repeat === true), JSON.stringify(timers));

p.event("twitch.redemption", red("r1", link(1)));
p.event("twitch.redemption", red("r2", link(2)));
p.event("twitch.redemption", red("r1", link(1)));          // повторная доставка
p.event("twitch.redemption", red("r9", link(9), "other")); // чужая награда
let q = p.route("queue").body.items;
check("события: две заявки, дубликат и чужая награда не попали", q.length === 2 && q[0].trackId === "10", JSON.stringify(q));

let n = p.route("next").body;
check("GET /next: первая заявка", n.item.id === 1 && n.item.albumId === "1" && n.resend === false, JSON.stringify(n));
n = p.route("next").body;
check("GET /next идемпотентен: та же заявка с resend", n.item.id === 1 && n.resend === true, JSON.stringify(n));
let r = p.route("started", { id: 1 });
check("POST /started → 200 и FULFILLED в Twitch", r.status === 200 && updates.includes("r1:FULFILLED"), JSON.stringify([r, updates]));
n = p.route("next").body;
check("после подтверждения уходит вторая", n.item.id === 2 && n.resend === false, JSON.stringify(n));

p.event("twitch.redemption", red("r3", "https://evil.example/x"));
check("плохая ссылка: отклонена, баллы возвращены (CANCELED)", updates.includes("r3:CANCELED"), JSON.stringify(updates));

p.event("timer", { id: "tick" });
check("таймер: плагин пишет состояние очереди в лог", logs.some((l) => l.msg === "тик очереди" && l.fields.sent === 1), JSON.stringify(logs.at(-1)));

// sad path
check("sad: старт неотданной заявки → 409", (() => { p.event("twitch.redemption", red("r4", link(4))); return p.route("started", { id: 4 }).status === 409; })());
check("sad: неизвестная заявка → 404", p.route("started", { id: 99 }).status === 404);
check("sad: плохое тело → 400", p.route("started", {}).status === 400);
check("sad: неизвестный обработчик → 404", p.route("nope").status === 404);
let crashed = false;
try { p.call("on_event", "not json"); p.event("twitch.redemption", "строка"); p.event("unknown.kind", {}); } catch { crashed = true; }
check("sad: мусорные события не роняют плагин", !crashed);

// перезапуск экземпляра: состояние в хосте
p = await instantiate(); p.init({ rewardId: "rw" });
n = p.route("next").body;
check("после перезапуска отданная заявка уходит повторно (resend)", n.item.id === 2 && n.resend === true, JSON.stringify(n));

console.log(ok ? "ALL OK" : "FAILED");
process.exit(ok ? 0 : 1);
