import type { PluginInfo, ToHost, ToPlugin } from "../../sdk/protocol.ts";

/**
 * Мост между плагинами-виджетами (iframe в sandbox) и сервером OC. Плагин не имеет доступа к API приложения и не открывает
 * своих сокетов: он шлёт сюда postMessage, а мы проверяем разрешения и переводим вызов в сообщения общего WebSocket /ws.
 * Событиям чата, наоборот, нужно одно соединение на страницу, а дальше мы раздаём их плагинам.
 *
 * От какого iframe пришло сообщение, определяем по ev.source (его подделать нельзя), а не по содержимому.
 */

interface Entry {
  frame: HTMLIFrameElement;
  info: PluginInfo;
  subs: Map<string, number>; // канал → сколько раз подписан
}

interface Pending {
  resolve: (r: Record<string, unknown>) => void;
  timer: ReturnType<typeof setTimeout>;
}

export interface HostOptions {
  /** Отправить сообщение серверу (JSON по /ws). */
  send: (msg: Record<string, unknown>) => void;
  /** false — предпросмотр (редактор): отправка в чат отключена, чтобы не дублировать ответы оверлея. */
  canSend: boolean;
}

const SERVER_EVENT_NAMES: Record<string, string> = { message: "chat.message", delete: "chat.delete", clear: "chat.clear", status: "chat.status" };

export class PluginHost {
  #entries = new Set<Entry>();
  #pending = new Map<number, Pending>();
  #nextReq = 1;

  constructor(private opts: HostOptions) {
    addEventListener("message", (ev) => this.#onMessage(ev));
  }

  register(frame: HTMLIFrameElement, info: Omit<PluginInfo, "preview">) {
    this.#entries.add({ frame, info: { ...info, preview: !this.opts.canSend }, subs: new Map() });
  }

  /** iframe закрывается: снимаем все его подписки. */
  unregister(frame: HTMLIFrameElement) {
    for (const e of this.#entries) {
      if (e.frame !== frame) continue;
      for (const [ch, n] of e.subs) for (let i = 0; i < n; i++) this.opts.send({ type: "chat:unsub", channel: ch });
      this.#entries.delete(e);
    }
  }

  /** Соединение /ws (пере)открылось: сервер ничего не помнит — подписываемся заново. */
  onConnected() {
    for (const e of this.#entries) {
      for (const [ch, n] of e.subs) for (let i = 0; i < n; i++) this.opts.send({ type: "chat:sub", reqId: 0, channel: ch });
    }
  }

  /** Сообщение сервера. Возвращает true, если оно относится к плагинам. */
  onServerMessage(msg: { type: string; [k: string]: unknown }): boolean {
    switch (msg.type) {
      case "chat:sub:result":
      case "chat:say:result":
      case "storage:result": {
        const p = this.#pending.get(msg.reqId as number);
        if (p) {
          this.#pending.delete(msg.reqId as number);
          clearTimeout(p.timer);
          p.resolve(msg);
        }
        return true;
      }
      case "storage:changed": {
        // Изменение данных плагина узнают все его копии (оверлей, предпросмотр в редакторе), включая ту, что записала.
        for (const e of this.#entries) {
          if (e.info.pluginId !== msg.plugin || !e.info.permissions.includes("storage")) continue;
          this.#toPlugin(e, { oc: 1, t: "event", name: "storage.change", data: { key: msg.key, deleted: msg.deleted, value: msg.value, rev: msg.rev } });
        }
        return true;
      }
      case "chat:event": {
        const name = SERVER_EVENT_NAMES[msg.event as string];
        if (!name) return true;
        const { type: _t, event: _e, ...data } = msg;
        const payload = msg.event === "message" ? msg.message : data;
        for (const e of this.#entries) {
          if (msg.event === "status" ? e.subs.size > 0 : e.subs.has(msg.channel as string)) this.#toPlugin(e, { oc: 1, t: "event", name, data: payload });
        }
        return true;
      }
    }
    return false;
  }

  // ---- от плагина ----

  #toPlugin(e: Entry, m: ToPlugin) {
    e.frame.contentWindow?.postMessage(m, "*"); // origin песочницы «null» — иначе адресат не указать
  }

  #onMessage(ev: MessageEvent) {
    const m = ev.data as ToHost | null;
    if (!m || m.oc !== 1) return;
    let entry: Entry | undefined;
    for (const e of this.#entries) if (e.frame.contentWindow === ev.source) entry = e;
    if (!entry) return;
    const e = entry;
    if (m.t === "hello") {
      this.#toPlugin(e, { oc: 1, t: "ready", info: e.info });
    } else if (m.t === "call" && typeof m.id === "number" && typeof m.method === "string") {
      const id = m.id;
      this.#handleCall(e, m.method, (m.params ?? {}) as Record<string, unknown>).then(
        (result) => this.#toPlugin(e, { oc: 1, t: "reply", id, ok: true, result }),
        (err: unknown) => this.#toPlugin(e, { oc: 1, t: "reply", id, ok: false, error: err instanceof Error ? err.message : String(err) }),
      );
    }
  }

  #need(e: Entry, perm: string) {
    if (!e.info.permissions.includes(perm)) throw new Error(`permission denied: ${perm}`);
  }

  /** Запрос серверу с ожиданием ответа. */
  #request(msg: Record<string, unknown>): Promise<Record<string, unknown>> {
    return new Promise((resolve, reject) => {
      const reqId = this.#nextReq++;
      const timer = setTimeout(() => {
        this.#pending.delete(reqId);
        reject(new Error("no answer from Overlay Composer server"));
      }, 25000);
      this.#pending.set(reqId, { resolve, timer });
      this.opts.send({ ...msg, reqId });
    });
  }

  async #handleCall(e: Entry, method: string, p: Record<string, unknown>): Promise<unknown> {
    const str = (v: unknown, name: string, max: number): string | undefined => {
      if (v === undefined || v === null || v === "") return undefined;
      if (typeof v !== "string" || v.length > max) throw new Error(`invalid ${name}`);
      return v;
    };
    switch (method) {
      case "chat.subscribe": {
        this.#need(e, "chat.read");
        const channel = str(p.channel, "channel", 64) ?? "";
        const r = await this.#request({ type: "chat:sub", channel });
        if (r.ok !== true) throw new Error(String(r.error ?? "subscribe failed"));
        const ch = r.channel as string;
        // плагин мог закрыться, пока ждали ответ: тогда подписку сразу снимаем
        if (!this.#entries.has(e)) this.opts.send({ type: "chat:unsub", channel: ch });
        else e.subs.set(ch, (e.subs.get(ch) ?? 0) + 1);
        return { channel: ch };
      }
      case "chat.unsubscribe": {
        this.#need(e, "chat.read");
        const ch = str(p.channel, "channel", 64)?.toLowerCase().replace(/^#/, "");
        const n = ch ? e.subs.get(ch) ?? 0 : 0;
        if (ch && n > 0) {
          if (n === 1) e.subs.delete(ch);
          else e.subs.set(ch, n - 1);
          this.opts.send({ type: "chat:unsub", channel: ch });
        }
        return undefined;
      }
      case "chat.say": {
        this.#need(e, "chat.send");
        if (!this.opts.canSend) throw new Error("chat.say is disabled in the editor preview");
        const text = str(p.text, "text", 2000);
        if (!text) throw new Error("invalid text");
        const as = str(p.as, "as", 16);
        if (as && as !== "bot" && as !== "streamer") throw new Error("invalid as");
        const r = await this.#request({
          type: "chat:say", text, channel: str(p.channel, "channel", 64) ?? "", as: as ?? "", replyTo: str(p.replyTo, "replyTo", 64) ?? "",
        });
        if (r.ok !== true) throw new Error(String(r.error ?? "send failed"));
        return undefined;
      }
      case "storage.get":
      case "storage.set":
      case "storage.delete":
      case "storage.list": {
        this.#need(e, "storage");
        const op = method.slice("storage.".length);
        const msg: Record<string, unknown> = { type: `storage:${op}`, plugin: e.info.pluginId };
        if (op === "list") {
          msg.prefix = str(p.prefix, "prefix", 128) ?? "";
          msg.limit = typeof p.limit === "number" ? Math.floor(p.limit) : 0;
        } else {
          msg.key = str(p.key, "key", 128);
          if (!msg.key) throw new Error("invalid key");
          if (op === "set") {
            if (!("value" in p) || p.value === undefined) throw new Error("invalid value");
            msg.value = p.value;
            if (p.ifRev !== undefined) {
              if (typeof p.ifRev !== "number" || !Number.isInteger(p.ifRev)) throw new Error("invalid ifRev");
              msg.ifRev = p.ifRev;
            }
          }
        }
        const r = await this.#request(msg);
        if (r.ok !== true) throw new Error(String(r.error ?? "storage error"));
        switch (op) {
          case "get": return { found: r.found === true, value: r.value, rev: r.rev ?? 0 };
          case "set": return { rev: r.rev };
          case "delete": return { deleted: r.deleted === true };
          default: return { entries: r.entries ?? [] };
        }
      }
    }
    throw new Error(`unknown method: ${method}`);
  }
}
