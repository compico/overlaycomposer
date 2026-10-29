// Аккаунты Twitch (стример/бот) и данные приложения Twitch. Секреты наружу не отдаются (только hasSecret).
import type { Api } from "../../lib/api.ts";
import { createStore, type Readable } from "../../lib/store.ts";

export type TwitchRole = "streamer" | "bot";

export interface TwitchAccount {
  role: string;
  login: string;
  scopes: string[];
  error?: string;
  /** Сервер: токен не обновляется или выданы не все права, которые нужны сейчас. */
  needsReconnect?: boolean;
  /** Каких прав не хватает (после обновления приложения с новыми функциями). */
  missingScopes?: string[];
}

export interface TwitchInfo {
  clientId: string;
  hasSecret: boolean;
  redirectUri: string;
  accounts: TwitchAccount[];
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

const ROLE_TITLE: Record<string, string> = { streamer: "Streamer", bot: "Bot" };

/** Что не так с аккаунтом, по-человечески; пусто — всё хорошо. */
export function accountProblem(a: TwitchAccount): string {
  if (a.error) return a.error;
  if (a.missingScopes?.length) return `new features need extra permissions (${a.missingScopes.join(", ")}) — reconnect the account`;
  return a.needsReconnect ? "reconnect the account" : "";
}

export interface ReconnectIssue {
  role: string;
  login: string;
  problem: string;
}

/** Аккаунты, которые нужно подключить заново (не подключённая роль — не проблема). null — раздел недоступен. */
export function reconnectIssues(info: TwitchInfo | null): ReconnectIssue[] {
  if (!info) return [];
  return info.accounts
    .filter((a) => a.needsReconnect || !!a.error || !!a.missingScopes?.length)
    .map((a) => ({ role: a.role, login: a.login, problem: accountProblem(a) }));
}

/** Бейдж в шапке: null — показывать нечего. */
export function reconnectBadge(issues: ReconnectIssue[]): { text: string; title: string } | null {
  if (issues.length === 0) return null;
  return {
    text: issues.length === 1 ? `Twitch: reconnect ${ROLE_TITLE[issues[0].role] ?? issues[0].role}` : "Twitch: reconnect",
    title: `${issues.map((i) => `${ROLE_TITLE[i.role] ?? i.role} (${i.login}): ${i.problem}`).join("\n")}\n\nClick to open Settings`,
  };
}
