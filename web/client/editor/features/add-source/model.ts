// Диалог «Add Source»: создание iframe-источника, выбор существующего, установка плагина как источника. Без DOM.
import type { LayoutObject, PluginManifest, Source } from "../../../../shared/types.ts";
import { uid } from "../../../../shared/types.ts";
import type { Api } from "../../lib/api.ts";
import { PERM_TEXT } from "../../lib/permissions.ts";
import { MIN_SIZE } from "../properties/model.ts";


const MASK = "•".repeat(8);

/**
 * Сегмент пути, похожий на секрет (ключ виджета, токен, UUID): в адресах вроде `/overlay/<id>/<apiKey>` токен лежит в пути,
 * а не в query. Эвристика только для показа: обычные слова и slug'и (`my-alerts`) не трогаем.
 */
function looksSecret(seg: string): boolean {
  if (/^[0-9a-f-]{32,}$/i.test(seg) && /[0-9a-f]{8}/i.test(seg)) return true; // UUID / hex-хэш
  return seg.length >= 20 && /^[A-Za-z0-9_]+$/.test(seg) && /[A-Za-z]/.test(seg) && /\d/.test(seg); // случайная строка букв и цифр
}

/** Скрывает в адресе всё, что может быть секретом: сегменты пути, похожие на токен, и всё после «?» и «#». */
export function maskUrl(u: string): string {
  const cut = u.search(/[?#]/);
  const head = cut < 0 ? u : u.slice(0, cut);
  const m = /^([a-z][a-z0-9+.-]*:\/\/[^/]*)(.*)$/i.exec(head);
  const origin = m ? m[1] : "";
  const path = (m ? m[2] : head).split("/").map((seg) => (looksSecret(seg) ? MASK : seg)).join("/");
  return cut < 0 ? origin + path : `${origin}${path}${u[cut]}${MASK}`;
}

/** Как показать адрес в поле: скрытый (только чтение) или целиком; и нужна ли кнопка «показать/скрыть». */
export function urlView(url: string, revealed: boolean): { text: string; readOnly: boolean; canToggle: boolean } {
  const masked = maskUrl(url);
  const canToggle = masked !== url;
  const hide = canToggle && !revealed;
  return { text: hide ? masked : url, readOnly: hide, canToggle };
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
