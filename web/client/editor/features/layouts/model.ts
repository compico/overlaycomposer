// Панель layout'ов: чистые функции — какие строки и бейджи показать. Состояние и действия — в features/editor.
import type { Layout } from "../../../../shared/types.ts";

export interface LayoutRow {
  id: string;
  name: string;
  /** Всплывающая подсказка у имени. */
  title: string;
  selected: boolean;
  isDefault: boolean;
  /** Число привязанных сцен OBS (показывается, если layout не default). */
  sceneCount: number;
  sceneList: string;
  live: boolean;
}

export function layoutRows(
  layouts: Pick<Layout, "id" | "name" | "scenes">[],
  currentId: string | null,
  defaultId: string | null,
  activeId: string | null,
): LayoutRow[] {
  return layouts.map((l) => ({
    id: l.id,
    name: l.name,
    title: l.scenes.length ? `OBS scenes: ${l.scenes.join(", ")}` : "No OBS scenes bound",
    selected: l.id === currentId,
    isDefault: l.id === defaultId,
    sceneCount: l.scenes.length,
    sceneList: l.scenes.join(", "),
    live: l.id === activeId,
  }));
}
