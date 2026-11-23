// Устаревшие плагины (deprecated.json источника, см. internal/market/deprecated.go): чистая логика без DOM.
import type { StoreEntry, StoreSource } from "./model.ts";

export type PluginKind = "widget" | "wasm";

export interface Replacement { id: string; kind: PluginKind; repo: string; url?: string }
export interface Deprecation { id: string; kind: PluginKind; since?: string; reason: string; replacedBy?: Replacement; repo: string }

/** Что показать пользователю как путь на замену. */
export type Alternative =
  | { type: "install"; label: string; entry: StoreEntry }   // замена-виджет есть в каталоге: ставится кнопкой
  | { type: "installed"; label: string }                    // замена уже установлена
  | { type: "store"; label: string; id: string }            // замена-wasm: ставится из магазина автономных плагинов в окне Plugins
  | { type: "link"; label: string; url: string };           // замена-виджет из чужого репозитория: страница, где её взять

const GITHUB_REPO = /^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/;

/** Адрес, куда вести за заменой: url из записи (только https) или страница репозитория (wasm — релизы). */
export function replacementUrl(r: Replacement | undefined): string | null {
  if (!r) return null;
  if (r.url && /^https:\/\/[^\s]+$/.test(r.url)) return r.url;
  if (!GITHUB_REPO.test(r.repo ?? "")) return null;
  return `https://github.com/${r.repo}${r.kind === "wasm" ? "/releases" : ""}`;
}

/** Запись об устаревании плагина: сначала из источника, установившего его (repo), иначе из любого источника. */
export function deprecationFor(sources: StoreSource[], kind: PluginKind, id: string, repo?: string): Deprecation | null {
  const mine = (s: StoreSource) => s.deprecated?.find((d) => d.kind === kind && d.id === id);
  const first = repo ? sources.find((s) => s.repo.toLowerCase() === repo.toLowerCase()) : undefined;
  const hit = first ? mine(first) : undefined;
  if (hit) return hit;
  for (const s of sources) {
    const d = mine(s);
    if (d) return d;
  }
  return null;
}

/** Путь на замену: установка из каталога, если замена — виджет из каталога; иначе ссылка; иначе ничего. */
export function alternativeFor(dep: Deprecation, sources: StoreSource[]): Alternative | null {
  const r = dep.replacedBy;
  if (!r) return null;
  if (r.kind === "widget") {
    for (const s of sources) {
      if (s.repo.toLowerCase() !== r.repo.toLowerCase()) continue;
      const e = s.plugins.find((p) => p.id === r.id);
      if (!e) continue;
      if (e.blocked) break;
      if (e.installed && !e.updateAvailable) return { type: "installed", label: `${e.name} is already installed` };
      return { type: "install", label: e.installed ? `Update ${e.name}` : `Install ${e.name}`, entry: e };
    }
  }
  if (r.kind === "wasm") return { type: "store", label: `Get ${r.id} in the plugin store`, id: r.id };
  const url = replacementUrl(r);
  return url ? { type: "link", label: `Get ${r.id}`, url } : null;
}

/** Строка про устаревание: «Deprecated since 2026-11-17: причина». */
export function deprecationText(d: Pick<Deprecation, "since" | "reason">): string {
  return `Deprecated${d.since ? ` since ${d.since}` : ""}: ${d.reason}`;
}
