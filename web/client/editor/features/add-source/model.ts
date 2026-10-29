// Диалог «Add Source»: создание iframe-источника, выбор существующего, установка плагина как источника. Без DOM.
import type { LayoutObject, PluginManifest, Source } from "../../../../shared/types.ts";
import { uid } from "../../../../shared/types.ts";
import type { Api } from "../../lib/api.ts";
import { PERM_TEXT } from "../../lib/permissions.ts";
import { MIN_SIZE } from "../properties/model.ts";


/** Всё после «?» (и «#») скрываем: в адресах виджетов часто лежат токены и секреты. */
export function maskUrl(u: string): string {
  const i = u.search(/[?#]/);
  return i < 0 ? u : `${u.slice(0, i + 1)}${"•".repeat(8)}`;
}

/** Свободное имя: «Name», «Name 2», «Name 3» … */
export function uniqueName(base: string, taken: string[]): string {
  let name = base, n = 2;
  while (taken.includes(name)) name = `${base} ${n++}`;
  return name;
}

/** Выбранный id, если он ещё есть в списке, иначе первый (или пусто). */
export function pickSelection(ids: string[], selected: string): string {
  return ids.includes(selected) ? selected : (ids[0] ?? "");
}

export const existingMeta = (s: Pick<Source, "type" | "data">): string =>
  s.type === "plugin" ? `plugin · ${s.data.plugin}` : maskUrl(s.data.url ?? "iframe");

export const pluginTitle = (p: Pick<PluginManifest, "name" | "version" | "builtin">): string =>
  `${p.name}${p.version ? ` ${p.version}` : ""}${p.builtin ? "" : " (custom)"}`;

/** Подсказка под списком плагинов. */
export function pluginHint(p: Pick<PluginManifest, "permissions"> | undefined): string {
  if (!p) return "";
  return p.permissions?.length
    ? `Asks for: ${p.permissions.map((x) => PERM_TEXT[x] ?? x).join("; ")}. You confirm this when placing it.`
    : "Asks for no permissions.";
}

export interface IframeInput {
  name: string;
  url: string;
  proxy: boolean;
  w: number;
  h: number;
}

export interface AddSourceDeps {
  api: Api;
  editor: {
    get(): { sources: Source[]; plugins: PluginManifest[] };
    loadSources(): Promise<void>;
    loadPlugins(): Promise<void>;
    addObject(sourceId: string, w?: number, h?: number): LayoutObject | null;
  };
  /** Разрешения выдаёт пользователь: плагин — чужой код. true — разрешить запрошенное. */
  confirmPermissions(plugin: PluginManifest, list: string[]): boolean;
}

export interface AddSourceModel {
  /** false — не хватает имени или адреса. */
  createIframe(input: IframeInput): Promise<boolean>;
  useExisting(id: string): void;
  /** false — такого плагина нет. */
  usePlugin(pluginId: string): Promise<boolean>;
}

export function createAddSourceModel(deps: AddSourceDeps): AddSourceModel {
  const { api, editor } = deps;
  return {
    async createIframe(i) {
      const name = i.name.trim(), url = i.url.trim();
      if (!name || !url) return false;
      const id = uid("src");
      await api("/api/sources", "POST", { id, name, type: "iframe", data: i.proxy ? { url, proxy: true } : { url } });
      await editor.loadSources();
      editor.addObject(id, Math.max(MIN_SIZE, i.w || 400), Math.max(MIN_SIZE, i.h || 300));
      return true;
    },
    useExisting(id) {
      if (id) editor.addObject(id);
    },
    async usePlugin(pluginId) {
      const p = editor.get().plugins.find((x) => x.id === pluginId);
      if (!p) return false;
      const granted = p.permissions?.length && deps.confirmPermissions(p, p.permissions) ? p.permissions : [];
      const name = uniqueName(p.name, editor.get().sources.map((x) => x.name));
      const id = uid("src");
      await api("/api/sources", "POST", { id, name, type: "plugin", data: { plugin: p.id, settings: {}, granted } });
      await editor.loadSources();
      editor.addObject(id, p.defaultSize.width, p.defaultSize.height);
      return true;
    },
  };
}
