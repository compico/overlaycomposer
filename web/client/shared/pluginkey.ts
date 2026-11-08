// Идентификация плагина-виджета. Обычный HTML-плагин — это id; автономный (wasm) плагин может нести несколько виджетов,
// поэтому его виджет определяется парой «плагин + виджет».

interface WidgetRef {
  id: string;
  widget?: string;
}

/** Ключ для списков и выбора: `id` или `id/виджет`. */
export const pluginKey = (p: WidgetRef): string => (p.widget ? `${p.id}/${p.widget}` : p.id);

/** Тот же ключ по данным источника (source.data). */
export const sourcePluginKey = (d: { plugin?: string; widget?: string }): string => pluginKey({ id: d.plugin ?? "", widget: d.widget });

/**
 * Адрес iframe виджета. Файлы обычного плагина раздаёт сервер из его папки (/plugins/<id>/), виджет автономного плагина
 * открывается через /plugins/<id>/widget/<виджет> (редирект на страницу из статики плагина). Настройки уходят в query.
 */
export function pluginFrameUrl(d: { plugin?: string; widget?: string; settings?: Record<string, string | number | boolean> }): string | null {
  if (!d.plugin) return null;
  const q = new URLSearchParams();
  for (const [k, v] of Object.entries(d.settings ?? {})) q.set(k, String(v));
  const qs = q.toString();
  const base = `/plugins/${encodeURIComponent(d.plugin)}/`;
  return `${base}${d.widget ? `widget/${encodeURIComponent(d.widget)}` : ""}${qs ? `?${qs}` : ""}`;
}
