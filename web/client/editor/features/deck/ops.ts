// Правка раскладки deck: чистые функции над данными, без DOM и сети. Каждая возвращает новую раскладку (или null, если действие
// невозможно: не помещается, пересекается, превышен предел). Правила повторяют проверку сервера (internal/deck), но сервер остаётся
// главным: он проверит раскладку ещё раз при сохранении.

export interface DeckTile {
  id: string;
  x: number;
  y: number;
  w: number;
  h: number;
  label?: string;
  color?: string;
  icon?: string;
  confirm?: boolean;
  action: { type: string; scene?: string };
}

export interface DeckGroup {
  id: string;
  name?: string;
  tiles: DeckTile[];
}

export interface DeckLayout {
  rev: number;
  groups: DeckGroup[];
}

export interface DeckSize { w: number; h: number }
export interface DeckGrid { rows: number; maxGroupCols: number; sizes: DeckSize[] }

export const MAX_GROUPS = 16;
export const MAX_TILES = 200;
export const MAX_LABEL = 40;
export const MAX_NAME = 24;

const clone = <T,>(v: T): T => JSON.parse(JSON.stringify(v)) as T;

export function groupWidth(g: DeckGroup): number {
  return g.tiles.reduce((w, t) => Math.max(w, t.x + t.w), 1);
}

export function tileCount(l: DeckLayout): number {
  return l.groups.reduce((n, g) => n + g.tiles.length, 0);
}

export function findTile(l: DeckLayout, id: string): { group: DeckGroup; tile: DeckTile } | null {
  for (const group of l.groups) {
    const tile = group.tiles.find((t) => t.id === id);
    if (tile) return { group, tile };
  }
  return null;
}

/** Помещается ли прямоугольник в группу (границы и пересечения); ignoreId — плитка, которую двигаем. */
export function fits(g: DeckGroup, r: { x: number; y: number; w: number; h: number }, grid: DeckGrid, ignoreId?: string): boolean {
  if (r.x < 0 || r.y < 0 || r.x + r.w > grid.maxGroupCols || r.y + r.h > grid.rows) return false;
  return !g.tiles.some((t) => t.id !== ignoreId && r.x < t.x + t.w && t.x < r.x + r.w && r.y < t.y + t.h && t.y < r.y + r.h);
}

/** Первое свободное место: столбцы слева направо, внутри столбца сверху вниз. */
export function findSpot(g: DeckGroup, size: DeckSize, grid: DeckGrid): { x: number; y: number } | null {
  for (let x = 0; x + size.w <= grid.maxGroupCols; x++) {
    for (let y = 0; y + size.h <= grid.rows; y++) {
      if (fits(g, { x, y, ...size }, grid)) return { x, y };
    }
  }
  return null;
}

/** Свободный id вида `prefix<N>` среди плиток (или групп). */
export function nextId(l: DeckLayout, prefix: "t" | "g"): string {
  const used = new Set(prefix === "g" ? l.groups.map((g) => g.id) : l.groups.flatMap((g) => g.tiles.map((t) => t.id)));
  for (let n = 1; ; n++) if (!used.has(`${prefix}${n}`)) return `${prefix}${n}`;
}

export function addGroup(l: DeckLayout, name = ""): { layout: DeckLayout; id: string } | null {
  if (l.groups.length >= MAX_GROUPS) return null;
  const layout = clone(l);
  const id = nextId(layout, "g");
  layout.groups.push({ id, name: name.slice(0, MAX_NAME), tiles: [] });
  return { layout, id };
}

export function renameGroup(l: DeckLayout, id: string, name: string): DeckLayout | null {
  const layout = clone(l);
  const g = layout.groups.find((x) => x.id === id);
  if (!g) return null;
  g.name = [...name.replace(/[\r\n\t]+/g, " ")].slice(0, MAX_NAME).join("");
  return layout;
}

/** Последнюю группу удалять можно: раскладка без групп сохранению не подлежит, но модель на это не опирается. */
export function removeGroup(l: DeckLayout, id: string): DeckLayout | null {
  if (!l.groups.some((g) => g.id === id)) return null;
  const layout = clone(l);
  layout.groups = layout.groups.filter((g) => g.id !== id);
  return layout;
}

export function moveGroup(l: DeckLayout, id: string, delta: -1 | 1): DeckLayout | null {
  const i = l.groups.findIndex((g) => g.id === id);
  const j = i + delta;
  if (i < 0 || j < 0 || j >= l.groups.length) return null;
  const layout = clone(l);
  [layout.groups[i], layout.groups[j]] = [layout.groups[j], layout.groups[i]];
  return layout;
}

export interface TileInit {
  scene?: string;
  label?: string;
  color?: string;
  icon?: string;
}

/** Новая плитка заданного размера на первом свободном месте группы. */
export function addTile(l: DeckLayout, groupId: string, size: DeckSize, init: TileInit, grid: DeckGrid): { layout: DeckLayout; id: string } | null {
  if (tileCount(l) >= MAX_TILES) return null;
  if (!grid.sizes.some((s) => s.w === size.w && s.h === size.h)) return null;
  const layout = clone(l);
  const g = layout.groups.find((x) => x.id === groupId);
  if (!g) return null;
  const spot = findSpot(g, size, grid);
  if (!spot) return null;
  const id = nextId(layout, "t");
  const t: DeckTile = { id, ...spot, ...size, action: { type: "scene", scene: init.scene ?? "" } };
  const label = (init.label ?? init.scene ?? "").slice(0, MAX_LABEL);
  if (label) t.label = label;
  if (init.color) t.color = init.color;
  if (init.icon) t.icon = init.icon;
  g.tiles.push(t);
  return { layout, id };
}

export type TilePatch = Partial<Pick<DeckTile, "label" | "color" | "icon" | "confirm" | "w" | "h">> & { scene?: string };

/** Изменить свойства плитки. Размер меняется, только если новая плитка помещается (иначе null). Пустые значения убираются из данных. */
export function updateTile(l: DeckLayout, id: string, patch: TilePatch, grid: DeckGrid): DeckLayout | null {
  const layout = clone(l);
  const found = findTile(layout, id);
  if (!found) return null;
  const { group, tile } = found;
  const w = patch.w ?? tile.w;
  const h = patch.h ?? tile.h;
  if (w !== tile.w || h !== tile.h) {
    if (!grid.sizes.some((s) => s.w === w && s.h === h)) return null;
    if (!fits(group, { x: tile.x, y: tile.y, w, h }, grid, id)) return null;
    tile.w = w;
    tile.h = h;
  }
  const text = (v: string | undefined, max: number) => [...(v ?? "").replace(/[\r\n\t]+/g, " ")].slice(0, max).join("");
  if (patch.label !== undefined) { const v = text(patch.label, MAX_LABEL); if (v) tile.label = v; else delete tile.label; }
  if (patch.color !== undefined) { if (patch.color) tile.color = patch.color; else delete tile.color; }
  if (patch.icon !== undefined) { if (patch.icon) tile.icon = patch.icon; else delete tile.icon; }
  if (patch.confirm !== undefined) { if (patch.confirm) tile.confirm = true; else delete tile.confirm; }
  if (patch.scene !== undefined) tile.action = { type: "scene", scene: patch.scene };
  return layout;
}

export function removeTile(l: DeckLayout, id: string): DeckLayout | null {
  if (!findTile(l, id)) return null;
  const layout = clone(l);
  for (const g of layout.groups) g.tiles = g.tiles.filter((t) => t.id !== id);
  return layout;
}

/** Перенос плитки в клетку (x, y) группы toGroup (возможно, другой). null — не помещается. */
export function moveTile(l: DeckLayout, id: string, toGroup: string, x: number, y: number, grid: DeckGrid): DeckLayout | null {
  const layout = clone(l);
  const found = findTile(layout, id);
  const dest = layout.groups.find((g) => g.id === toGroup);
  if (!found || !dest) return null;
  const { group, tile } = found;
  if (!fits(dest, { x, y, w: tile.w, h: tile.h }, grid, id)) return null;
  if (group !== dest) {
    group.tiles = group.tiles.filter((t) => t.id !== id);
    dest.tiles.push(tile);
  }
  tile.x = x;
  tile.y = y;
  return layout;
}

/** Размеры, в которые плитку можно превратить, не задев соседей. */
export function sizesFor(l: DeckLayout, id: string, grid: DeckGrid): DeckSize[] {
  const f = findTile(l, id);
  if (!f) return [];
  return grid.sizes.filter((s) => fits(f.group, { x: f.tile.x, y: f.tile.y, ...s }, grid, id));
}

/** Сцены OBS, которых ещё нет ни на одной плитке (по порядку OBS). */
export function unusedScenes(l: DeckLayout, scenes: string[]): string[] {
  const used = new Set(l.groups.flatMap((g) => g.tiles.map((t) => t.action.scene ?? "")));
  return scenes.filter((s) => !used.has(s));
}
