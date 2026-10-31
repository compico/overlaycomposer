// Панель слоёв: чистые функции — какие строки показать. Состояние и действия — в features/editor.
import type { LayoutObject, Source } from "../../../../shared/types.ts";

export interface LayerRow {
  id: string;
  name: string;
  visible: boolean;
  locked: boolean;
  selected: boolean;
}

/** Отображаемое имя слоя: своё имя, иначе имя источника, иначе пометка о пропавшем источнике. */
export function layerName(o: Pick<LayoutObject, "name" | "sourceId">, sources: Pick<Source, "id" | "name">[]): string {
  return o.name || (sources.find((s) => s.id === o.sourceId)?.name ?? `(missing ${o.sourceId})`);
}

/** Строки панели: верхний слой (больший zIndex) первым. */
export function layerRows(objects: LayoutObject[], sources: Pick<Source, "id" | "name">[], selectedId: string | null): LayerRow[] {
  return [...objects]
    .sort((a, b) => b.zIndex - a.zIndex)
    .map((o) => ({ id: o.id, name: layerName(o, sources), visible: o.visible, locked: o.locked, selected: o.id === selectedId }));
}
