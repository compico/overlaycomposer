// Автономные (wasm) плагины в магазине: каталог из релизов источников, установка, обновление, смена версии. Без DOM.
import type { Api } from "../../lib/api.ts";
import { createStore, type Readable } from "../../lib/store.ts";
import type { WasmChange, WasmVersion } from "./wasm-versions.ts";

export interface WasmEntry {
  id: string; name: string; version: string; description?: string; author?: string; permissions?: string[];
  repo: string; tag: string; sha256?: string; size?: number;
  /** У последнего релиза нет <id>.json: имя, права и sha256 неизвестны. */
  noInfo?: boolean;
  versions: WasmVersion[];
  /** Журнал изменений последнего релиза (вся история); приходит только для установленных плагинов. */
  changelog?: WasmChange[];
  installed: boolean; installedVersion?: string; installedTag?: string; updateAvailable: boolean;
  /** Права последней версии, которых нет у установленной. */
  addedPermissions?: string[];
  blocked?: "foreign";
}
export interface WasmSource { repo: string; error?: string; plugins: WasmEntry[] }

export interface WasmInstalled {
  id: string; version: string; tag: string; sha256: string;
  previous?: string;
  /** Права новой версии, которых не было у прежней: не выданы, пока пользователь не разрешит. */
  addedPermissions?: string[];
}

export interface WasmStoreState { loading: boolean; error: string | null; sources: WasmSource[] }

export interface WasmStoreModel extends Readable<WasmStoreState> {
  load(refresh?: boolean): Promise<void>;
  /** Поставить, обновить или сменить версию (tag — релиз `<id>-v<версия>`); sha256 — отпечаток из каталога, если он известен. */
  install(p: { repo: string; id: string; tag: string; sha256?: string }): Promise<WasmInstalled>;
  uninstall(id: string): Promise<void>;
}

export interface WasmStoreDeps {
  api: Api;
  /** Набор плагинов на диске изменился: перечитать список «Plugins». */
  afterChange(): Promise<void>;
}

export function createWasmStoreModel({ api, afterChange }: WasmStoreDeps): WasmStoreModel {
  const state = createStore<WasmStoreState>({ loading: false, error: null, sources: [] });

  async function load(refresh = false) {
    state.update((s) => ({ ...s, loading: true }));
    try {
      const d = await api<{ sources: WasmSource[] }>(`/api/market/wasm${refresh ? "?refresh=1" : ""}`);
      state.set({ loading: false, error: null, sources: d.sources ?? [] });
    } catch (e) {
      state.set({ loading: false, error: e instanceof Error ? e.message : String(e), sources: state.get().sources });
    }
  }

  /** Изменение на сервере; в любом случае (и при ошибке тоже) перечитываем каталог, чтобы UI отражал реальность. */
  async function mutate<T>(fn: () => Promise<T>): Promise<T> {
    try { return await fn(); } finally { await load(); }
  }

  return {
    get: state.get,
    subscribe: state.subscribe,
    load,
    install: (p) => mutate(async () => {
      const res = await api<WasmInstalled>("/api/market/wasm/install", "POST", { repo: p.repo, id: p.id, tag: p.tag, ...(p.sha256 ? { sha256: p.sha256 } : {}) });
      await afterChange();
      return res;
    }),
    uninstall: (id) => mutate(async () => {
      await api("/api/market/wasm/uninstall", "POST", { id });
      await afterChange();
    }),
  };
}
