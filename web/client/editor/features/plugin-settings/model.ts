// Настройки плагина-виджета: схема из plugin.json → значения в source.data.settings, разрешения, данные плагина. Без DOM.
import type { Source } from "../../../../shared/types.ts";
import type { Api } from "../../lib/api.ts";

export type SettingValue = string | number | boolean;

/** Новые настройки: undefined удаляет ключ (сброс к умолчанию). Исходный объект не меняется. */
export function nextSettings(cur: Record<string, SettingValue> | undefined, key: string, v: SettingValue | undefined): Record<string, SettingValue> {
  const next = { ...(cur ?? {}) };
  if (v === undefined) delete next[key]; else next[key] = v;
  return next;
}

export function nextGranted(cur: string[] | undefined, perm: string, on: boolean): string[] {
  const set = new Set(cur ?? []);
  if (on) set.add(perm); else set.delete(perm);
  return [...set];
}

export const storageText = (st: { keys: number; bytes: number }) => `Plugin data: ${st.keys} keys, ${(st.bytes / 1024).toFixed(1)} KB`;

export const isHexColor = (v: unknown): v is string => typeof v === "string" && /^#[0-9a-f]{6}$/i.test(v);

export interface PluginSettingsModel {
  /** Сохранить одно значение и обновить source ответом сервера. */
  saveSetting(src: Source, key: string, v: SettingValue | undefined): Promise<void>;
  setGranted(src: Source, perm: string, on: boolean): Promise<void>;
  storage(pluginId: string): Promise<{ keys: number; bytes: number }>;
  eraseStorage(pluginId: string): Promise<void>;
}

export function createPluginSettingsModel(api: Api): PluginSettingsModel {
  const put = (src: Source, data: Record<string, unknown>) =>
    api<Source>(`/api/sources/${encodeURIComponent(src.id)}`, "PUT", { data });
  const storageUrl = (id: string) => `/api/plugins/${encodeURIComponent(id)}/storage`;
  return {
    async saveSetting(src, key, v) {
      const r = await put(src, { settings: nextSettings(src.data.settings, key, v) });
      src.data.settings = r.data.settings ?? {};
    },
    async setGranted(src, perm, on) {
      const r = await put(src, { granted: nextGranted(src.data.granted, perm, on) });
      src.data.granted = r.data.granted ?? [];
    },
    storage: (id) => api<{ keys: number; bytes: number }>(storageUrl(id)),
    async eraseStorage(id) { await api(storageUrl(id), "DELETE"); },
  };
}
