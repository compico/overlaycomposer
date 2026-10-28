// Магазин плагинов: состояние каталога и действия без DOM.
import type { Api } from "../../lib/api.ts";
import { createStore, type Readable } from "../../lib/store.ts";

export interface StoreEntry {
  id: string; name: string; version?: string; description?: string; author?: string; permissions?: string[]; repo: string;
  installed: boolean; installedVersion?: string; installedCommit?: string; updateAvailable: boolean; blocked?: "builtin" | "foreign";
  digest: string; commit?: string; addedPermissions?: string[];
}
export interface StoreSource { repo: string; error?: string; plugins: StoreEntry[] }
export interface StoreData { sources: StoreSource[]; installed: Record<string, { repo: string; version?: string }>; hasToken: boolean }

export interface StoreState {
  loading: boolean;
  /** Каталог не загрузился (сервер недоступен и т. п.). */
  error: string | null;
  data: StoreData | null;
}

export interface StoreModel extends Readable<StoreState> {
  load(refresh?: boolean): Promise<void>;
  addSource(repo: string): Promise<void>;
  removeSource(repo: string): Promise<void>;
  saveToken(token: string): Promise<void>;
  clearToken(): Promise<void>;
  /** Установить или обновить плагин; после успеха вызывает afterChange (перечитать плагины/виджеты в редакторе). */
  install(p: Pick<StoreEntry, "repo" | "id" | "digest">): Promise<void>;
  uninstall(id: string): Promise<void>;
}

export interface StoreDeps {
  api: Api;
  /** Плагин поменялся на диске: подхватить в редакторе. */
  afterChange(): Promise<void>;
}

export function createStoreModel({ api, afterChange }: StoreDeps): StoreModel {
  const state = createStore<StoreState>({ loading: false, error: null, data: null });

  async function load(refresh = false) {
    state.update((s) => ({ ...s, loading: true }));
    try {
      const data = await api<StoreData>(`/api/market${refresh ? "?refresh=1" : ""}`);
      state.set({ loading: false, error: null, data });
    } catch (e) {
      state.set({ loading: false, error: e instanceof Error ? e.message : String(e), data: state.get().data });
    }
  }

  /** Изменение на сервере; в любом случае (и при ошибке тоже) перечитываем каталог, чтобы UI отражал реальность. */
  async function mutate(fn: () => Promise<unknown>, refresh = false) {
    try { await fn(); } finally { await load(refresh); }
  }

  return {
    get: state.get,
    subscribe: state.subscribe,
    load,
    addSource: (repo) => mutate(() => api("/api/market/sources", "POST", { repo })),
    removeSource: (repo) => mutate(() => api(`/api/market/sources?repo=${encodeURIComponent(repo)}`, "DELETE")),
    saveToken: (token) => mutate(() => api("/api/market/token", "PUT", { token }), true),
    clearToken: () => mutate(() => api("/api/market/token", "DELETE"), true),
    install: (p) => mutate(async () => {
      await api("/api/market/install", "POST", { repo: p.repo, id: p.id, digest: p.digest });
      await afterChange();
    }),
    uninstall: (id) => mutate(async () => {
      await api("/api/market/uninstall", "POST", { id });
      await afterChange();
    }),
  };
}
