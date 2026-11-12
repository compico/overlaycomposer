// Редактор раскладки deck: состояние (рабочая копия, «есть несохранённое») и работа с сервером. Правка данных — в ops.ts.
import { ApiError, isConflict, type Api } from "../../lib/api.ts";
import { createStore, type Readable } from "../../lib/store.ts";
import {
  addGroup, addTile, moveGroup, moveTile, removeGroup, removeTile, renameGroup, updateTile, unusedScenes,
  type DeckGrid, type DeckLayout, type DeckSize, type TilePatch,
} from "./ops.ts";

export interface DeckState {
  loaded: boolean;
  /** Рабочая копия раскладки (null, пока не загружена). */
  layout: DeckLayout | null;
  /** Сохранена ли раскладка на сервере (false — показывается раскладка по умолчанию, пока ничего не сохранили). */
  saved: boolean;
  dirty: boolean;
  scenes: string[];
  /** Цвета Metro: имя → CSS-цвет (список приходит с сервера, чтобы совпадал с тем, что рисует /deck). */
  palette: Record<string, string>;
  /** Значки: имя → символ. */
  icons: Record<string, string>;
  grid: DeckGrid;
  selected: string | null;
  error: string;
}

interface LayoutResponse {
  layout: DeckLayout;
  saved: boolean;
  scenes: string[] | null;
  palette: Record<string, string>;
  icons: Record<string, string>;
  grid: DeckGrid;
}

export interface DeckModel extends Readable<DeckState> {
  load(): Promise<void>;
  /** Сохранить; при конфликте версий (раскладку изменили в другом месте) бросает ApiError 409. */
  save(): Promise<void>;
  /** Вернуть раскладку по умолчанию (плитка на каждую сцену). */
  reset(): Promise<void>;
  select(id: string | null): void;
  addGroup(): string | null;
  renameGroup(id: string, name: string): void;
  removeGroup(id: string): void;
  moveGroup(id: string, delta: -1 | 1): void;
  /** Добавить плитку; сцена берётся первая ещё не использованная. Возвращает id или null (нет места / предел). */
  addTile(groupId: string, size: DeckSize): string | null;
  updateTile(id: string, patch: TilePatch): boolean;
  removeTile(id: string): void;
  moveTile(id: string, groupId: string, x: number, y: number): boolean;
}

const DEFAULT_COLORS = ["blue", "teal", "green", "orange", "purple", "magenta", "steel", "red", "cyan", "lime", "brown", "yellow"];

const empty = (): DeckState => ({
  loaded: false, layout: null, saved: false, dirty: false, scenes: [], palette: {}, icons: {},
  grid: { rows: 4, maxGroupCols: 16, sizes: [{ w: 1, h: 1 }, { w: 2, h: 1 }, { w: 3, h: 1 }, { w: 2, h: 3 }] }, selected: null, error: "",
});

export function createDeckModel(api: Api): DeckModel {
  const store = createStore<DeckState>(empty());
  const s = () => store.get();

  /** Применить результат правки: null — действие невозможно, состояние не меняется. */
  function apply(next: DeckLayout | null, selected?: string | null): boolean {
    if (!next) return false;
    store.set({ ...s(), layout: next, dirty: true, selected: selected === undefined ? s().selected : selected });
    return true;
  }

  async function load() {
    try {
      const r = await api<LayoutResponse>("/api/deck/layout");
      for (const g of r.layout.groups) g.tiles ??= []; // страховка: старый сервер отдавал null для пустой группы
      store.set({
        loaded: true, layout: r.layout, saved: r.saved, dirty: false, scenes: r.scenes ?? [], palette: r.palette ?? {}, icons: r.icons ?? {},
        grid: { ...r.grid, sizes: r.grid.sizes ?? s().grid.sizes }, selected: null, error: "",
      });
    } catch (e) {
      store.set({ ...s(), error: e instanceof Error ? e.message : String(e) });
      throw e;
    }
  }

  return {
    get: store.get,
    subscribe: store.subscribe,
    load,
    async save() {
      const l = s().layout;
      if (!l) return;
      const r = await api<{ rev: number }>("/api/deck/layout", "PUT", l);
      store.set({ ...s(), layout: { ...l, rev: r.rev }, saved: true, dirty: false });
    },
    async reset() {
      await api("/api/deck/layout", "DELETE");
      await load();
    },
    select: (id) => store.set({ ...s(), selected: id }),
    addGroup() {
      const l = s().layout;
      const r = l && addGroup(l);
      if (!r) return null;
      apply(r.layout, null);
      return r.id;
    },
    renameGroup: (id, name) => { const l = s().layout; if (l) apply(renameGroup(l, id, name)); },
    removeGroup(id) {
      const l = s().layout;
      if (!l) return;
      const gone = l.groups.find((g) => g.id === id)?.tiles.some((t) => t.id === s().selected);
      apply(removeGroup(l, id), gone ? null : undefined);
    },
    moveGroup: (id, d) => { const l = s().layout; if (l) apply(moveGroup(l, id, d)); },
    addTile(groupId, size) {
      const st = s();
      if (!st.layout) return null;
      const scene = unusedScenes(st.layout, st.scenes)[0];
      const index = st.layout.groups.reduce((n, g) => n + g.tiles.length, 0);
      const colors = Object.keys(st.palette).length ? DEFAULT_COLORS.filter((c) => c in st.palette) : DEFAULT_COLORS;
      const r = addTile(st.layout, groupId, size, { scene, label: scene, color: colors[index % colors.length] }, st.grid);
      if (!r) return null;
      apply(r.layout, r.id);
      return r.id;
    },
    updateTile(id, patch) {
      const st = s();
      return !!st.layout && apply(updateTile(st.layout, id, patch, st.grid));
    },
    removeTile(id) {
      const l = s().layout;
      if (l) apply(removeTile(l, id), s().selected === id ? null : undefined);
    },
    moveTile(id, groupId, x, y) {
      const st = s();
      return !!st.layout && apply(moveTile(st.layout, id, groupId, x, y, st.grid));
    },
  };
}

export { ApiError, isConflict };
