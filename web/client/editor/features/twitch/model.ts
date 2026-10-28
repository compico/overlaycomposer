// Аккаунты Twitch (стример/бот) и данные приложения Twitch. Секреты наружу не отдаются (только hasSecret).
import type { Api } from "../../lib/api.ts";
import { createStore, type Readable } from "../../lib/store.ts";

export type TwitchRole = "streamer" | "bot";

export interface TwitchInfo {
  clientId: string;
  hasSecret: boolean;
  redirectUri: string;
  accounts: { role: string; login: string; scopes: string[]; error?: string }[];
}

export interface TwitchModel extends Readable<TwitchInfo | null> {
  /** null в состоянии — раздел недоступен (с другого компьютера сервер отвечает 403): представление его скрывает. */
  load(): Promise<void>;
  saveApp(clientId: string, clientSecret: string): Promise<void>;
  /** Одноразовая ссылка авторизации аккаунта (действует 10 минут). */
  connectLink(role: TwitchRole): Promise<string>;
  disconnect(role: TwitchRole): Promise<void>;
}

export function createTwitchModel(api: Api): TwitchModel {
  const state = createStore<TwitchInfo | null>(null);
  const model: TwitchModel = {
    get: state.get,
    subscribe: state.subscribe,
    async load() {
      try { state.set(await api<TwitchInfo>("/api/twitch")); } catch { state.set(null); }
    },
    async saveApp(clientId, clientSecret) {
      await api("/api/twitch/app", "PUT", { clientId, clientSecret });
      await model.load();
    },
    async connectLink(role) {
      const r = await api<{ url: string }>(`/api/twitch/connect/${role}`, "POST", {});
      return r.url;
    },
    async disconnect(role) {
      await api(`/api/twitch/accounts/${role}`, "DELETE");
      await model.load();
    },
  };
  return model;
}
