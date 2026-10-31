// Сведения о приложении (версия, пути, автозапуск, трей) и действия «открыть папку» / «выйти».
import type { Api } from "../../lib/api.ts";
import { createStore, type Readable } from "../../lib/store.ts";

export interface AppInfo {
  compiled: boolean;
  os: string;
  configFile: string;
  dataDir: string;
  logFile: string;
  autostart: boolean | null; // null — не поддерживается
  tray: boolean;
  trayAvailable: boolean;
  version?: string;
  update?: { latest: string; url: string; notes: string; available: boolean } | null;
}

export interface AppModel extends Readable<AppInfo | null> {
  /** Перечитать; старый/недоступный сервер — не ошибка (остаётся прежнее значение). */
  load(): Promise<void>;
  setAutostart(enabled: boolean): Promise<void>;
  open(what: "data" | "logs" | "plugins"): Promise<void>;
  /** Остановить приложение; обрыв соединения после этого — норма. */
  quit(): Promise<void>;
}

export function createAppModel(api: Api): AppModel {
  const state = createStore<AppInfo | null>(null);
  return {
    get: state.get,
    subscribe: state.subscribe,
    async load() {
      try { state.set(await api<AppInfo>("/api/app")); } catch { /* старый сервер */ }
    },
    async setAutostart(enabled) {
      await api("/api/app/autostart", "PUT", { enabled });
      await this.load();
    },
    async open(what) { await api("/api/app/open", "POST", { what }); },
    async quit() {
      try { await api("/api/app/quit", "POST", {}); } catch { /* сервер уже закрылся */ }
    },
  };
}
