// Настройки сервера и OBS (config.json) и статус подключения.
import type { ObsInfo } from "../../../../shared/types.ts";
import type { Api } from "../../lib/api.ts";
import { createStore, type Readable } from "../../lib/store.ts";

export interface ConfigView {
  host: string;
  port: number;
  debugProxy: boolean;
  obsEnabled: boolean;
  obsUrl: string;
  hasObsPassword: boolean;
  obs: ObsInfo;
  obsWsConnected: boolean;
  obsWsError: string | null;
  overlayUrl: string;
  restartRequired: boolean;
}

export interface ConfigPatch {
  host: string;
  port: number;
  debugProxy: boolean;
  obs: { enabled: boolean; url: string; password: string; clearPassword: boolean };
}

export interface ConfigModel extends Readable<ConfigView | null> {
  /** Загрузить; ошибка пробрасывается (при открытии настроек она показывается пользователю). */
  load(): Promise<ConfigView>;
  /** Перечитать статус тихо (сервер может перезапускаться). */
  refresh(): Promise<void>;
  save(patch: ConfigPatch): Promise<ConfigView>;
  reconnectObs(): Promise<void>;
}

export function createConfigModel(api: Api): ConfigModel {
  const state = createStore<ConfigView | null>(null);
  return {
    get: state.get,
    subscribe: state.subscribe,
    async load() {
      const c = await api<ConfigView>("/api/config");
      state.set(c);
      return c;
    },
    async refresh() {
      try { state.set(await api<ConfigView>("/api/config")); } catch { /* сервер перезапускается */ }
    },
    async save(patch) {
      const c = await api<ConfigView>("/api/config", "PUT", patch);
      state.set(c);
      return c;
    },
    async reconnectObs() { await api("/api/obs/reconnect", "POST", {}); },
  };
}
