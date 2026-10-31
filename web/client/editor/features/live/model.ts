// Живая связь с сервером (/ws): как реагировать на сообщения. Без DOM и сокетов — порты подставляются снаружи.
import type { ObsInfo, ServerMessage } from "../../../../shared/types.ts";

export interface LivePorts {
  /** Какой layout сейчас показывается в /overlay. */
  setActive(layoutId: string | null): void;
  /** Статус OBS и список сцен. */
  obs(info: ObsInfo): void;
  /** Сцена OBS сменилась (для бейджа). */
  scene(name: string): void;
  loadLayouts(): Promise<void>;
  loadSources(): Promise<void>;
  reloadCurrent(): Promise<void>;
  /** Открытый layout и есть ли в нём несохранённые правки. */
  current(): { id: string; dirty: boolean } | null;
}

/**
 * Обработчик сообщений сервера. Чужое изменение открытого layout подтягивается, только если нет несохранённых правок.
 * Ошибки обработки не должны ронять соединение — их ловит вызывающий.
 */
export function createLiveHandler(p: LivePorts): (msg: ServerMessage) => Promise<void> {
  return async (msg) => {
    switch (msg.type) {
      case "hello":
        p.setActive(msg.layoutId);
        p.obs(msg.obs);
        break;
      case "obs:status":
        p.obs(msg.obs);
        break;
      case "scene:changed":
        p.setActive(msg.layoutId ?? null);
        if (msg.scene) p.scene(msg.scene);
        break;
      case "layouts:changed":
        await p.loadLayouts();
        break;
      case "sources:changed":
        await p.loadSources();
        break;
      case "layout:updated": {
        const cur = p.current();
        if (cur && msg.layoutId === cur.id && !cur.dirty) await p.reloadCurrent();
        break;
      }
    }
  };
}
