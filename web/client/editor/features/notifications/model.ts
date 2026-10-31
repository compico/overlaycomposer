// Центр уведомлений: состояние и действия без DOM (см. ../../README.md).
import type { Api } from "../../lib/api.ts";
import { createStore, type Readable } from "../../lib/store.ts";

export interface NotifItem { key: string; kind: string; title: string; text?: string; at: number; read: boolean; data?: Record<string, unknown> }
export interface NotifState { items: NotifItem[]; unread: number; lastCheck: number }
export interface PluginChange { version: string; date?: string; changes?: string[]; migration?: string }

/** Публичный интерфейс модели — единственное, что видит представление. */
export interface NotificationsModel extends Readable<NotifState> {
  /** Тихо перечитать с сервера (старый/недоступный сервер — не ошибка). */
  load(): Promise<void>;
  markRead(key: string): Promise<void>; // пустой ключ — все
  dismiss(key: string): Promise<void>;
  /** Проверить обновления плагинов сейчас; ошибки (в т.ч. «слишком часто») пробрасываются. */
  check(): Promise<void>;
}

export function createNotificationsModel(api: Api): NotificationsModel {
  const store = createStore<NotifState>({ items: [], unread: 0, lastCheck: 0 });
  const call = async (path: string, body: unknown = {}) => store.set(await api<NotifState>(path, "POST", body));
  return {
    get: store.get,
    subscribe: store.subscribe,
    async load() {
      try { store.set(await api<NotifState>("/api/notifications")); } catch { /* сервер старый или недоступен */ }
    },
    markRead: (key) => call("/api/notifications/read", { key }),
    dismiss: (key) => call("/api/notifications/dismiss", { key }),
    check: () => call("/api/notifications/check"),
  };
}
