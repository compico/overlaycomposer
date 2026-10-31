import type { ServerMessage } from "../../../../shared/types.ts";

export interface LiveConnection {
  send(msg: unknown): void;
}

export interface LiveOptions {
  /** Сообщение сервера. Возвращает true, если его забрал кто-то другой (например, хост плагинов). */
  intercept?(msg: ServerMessage): boolean;
  handle(msg: ServerMessage): Promise<void>;
  onOpen?(): void;
  retryMs?: number;
}

/** WebSocket /ws с переподключением. */
export function connectLive(opts: LiveOptions): LiveConnection {
  let ws: WebSocket | null = null;
  const connect = () => {
    ws = new WebSocket(`${location.protocol === "https:" ? "wss" : "ws"}://${location.host}/ws`);
    ws.onopen = () => opts.onOpen?.();
    ws.onmessage = async (ev) => {
      const msg = JSON.parse(ev.data) as ServerMessage;
      if (opts.intercept?.(msg)) return;
      try { await opts.handle(msg); } catch (e) { console.warn(e); }
    };
    ws.onclose = () => setTimeout(connect, opts.retryMs ?? 1500);
  };
  connect();
  return {
    send(msg) { if (ws?.readyState === WebSocket.OPEN) ws.send(JSON.stringify(msg)); },
  };
}
