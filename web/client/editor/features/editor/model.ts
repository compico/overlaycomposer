// Модель редактора: рабочая копия layout, выбор, «несохранённое», история правок, загрузка и сохранение. Без DOM.
//
// Рабочая копия (`layout`) правится на месте: жесты на канвасе меняют transform каждый кадр, поэтому полная
// неизменяемость тут не нужна. Вместо неё — явный сигнал: любое изменение приходит подписчикам с полем `change`,
// по которому представление решает, что перерисовать (см. ChangeKind).
import type { Layout, LayoutObject, ObsInfo, PluginManifest, Source } from "../../../../shared/types.ts";
import { defaultTransform, uid } from "../../../../shared/types.ts";
import { History } from "../../../../shared/history.ts";
import type { Api } from "../../lib/api.ts";
import { createStore, type Readable } from "../../lib/store.ts";

/**
 * Что изменилось (чтобы представление не перерисовывало всё подряд):
 *  reset      — открыт/перезагружен layout, отмена/повтор: перерисовать всё;
 *  structure  — добавлены/удалены/переупорядочены/скрыты слои: канвас, слои, свойства;
 *  layers     — блокировка/переименование слоя: рамки на канвасе и список слоёв;
 *  selection  — сменился выбор;
 *  geometry   — правка на месте (жест на канвасе): только бейджи;
 *  meta       — список layout, default/live, имя, привязки сцен;
 *  data       — перечитаны sources/plugins.
 */
export type ChangeKind = "reset" | "structure" | "layers" | "selection" | "geometry" | "meta" | "data";

export interface EditorState {
  layouts: Layout[];
  sources: Source[];
  plugins: PluginManifest[];
  layout: Layout | null;
  selectedId: string | null;
  dirty: boolean;
  defaultLayoutId: string | null;
  activeLayoutId: string | null;
  obsScenes: string[];
  canUndo: boolean;
  canRedo: boolean;
  change: ChangeKind;
  /** Растёт при каждом изменении — удобно для `useSyncExternalStore`/memo во фреймворке. */
  rev: number;
}

export interface EditorDeps {
  api: Api;
  /** Спросить, можно ли выбросить несохранённые правки (в UI — confirm). */
  confirmDiscard(): boolean;
  /** Пауза склейки быстрых правок в один шаг истории. */
  recordDelayMs?: number;
}

// ---- чистые операции над layout (тестируются отдельно) ----

export function maxZ(l: Layout): number {
  return Math.max(0, ...l.objects.map((o) => o.zIndex));
}

/** Новый слой по центру холста поверх остальных. */
export function newObject(l: Layout, sourceId: string, name: string | undefined, w = 400, h = 300): LayoutObject {
  const t = { ...defaultTransform(), width: w, height: h };
  t.x = Math.round((l.canvas.width - w) / 2);
  t.y = Math.round((l.canvas.height - h) / 2);
  return { id: uid("obj"), name, sourceId, transform: t, opacity: 1, visible: true, locked: false, zIndex: maxZ(l) + 1 };
}

/** Переносит `movedId` непосредственно над `beforeId` в отображаемом порядке (верх = больший zIndex). */
export function reorderObjects(l: Layout, movedId: string, beforeId: string): boolean {
  const order = [...l.objects].sort((a, b) => b.zIndex - a.zIndex).map((o) => o.id);
  const from = order.indexOf(movedId), to = order.indexOf(beforeId);
  if (from < 0 || to < 0 || movedId === beforeId) return false;
  order.splice(from, 1);
  order.splice(order.indexOf(beforeId), 0, movedId);
  const n = order.length;
  order.forEach((id, i) => { l.objects.find((x) => x.id === id)!.zIndex = n - i; });
  return true;
}

// ---- модель ----

export interface EditorModel extends Readable<EditorState> {
  // загрузка
  loadSources(): Promise<void>;
  loadPlugins(): Promise<void>;
  loadLayouts(): Promise<void>;
  /** Первичная загрузка; возвращает ObsInfo из /api/state (для статуса). */
  bootstrap(): Promise<ObsInfo>;
  /** false — пользователь отказался выбросить несохранённое. */
  openLayout(id: string, force?: boolean): Promise<boolean>;
  /** Подтянуть чужое обновление текущего layout (только если нет несохранённых правок). */
  reloadCurrent(): Promise<void>;
  save(): Promise<void>;
  // выбор и правки
  select(id: string | null): void;
  /** Вызвать после правки на месте (жест, поле свойств): «несохранённое» + запись в историю с задержкой. */
  touch(): void;
  /** Правка, не требующая перерисовки (привязки сцен и т. п.). */
  markDirty(change?: ChangeKind): void;
  addObject(sourceId: string, w?: number, h?: number): LayoutObject | null;
  /** Добавить готовый объект (вставка из буфера) и выбрать его. */
  insertObject(o: LayoutObject): void;
  removeObject(id: string): void;
  /** Убрать слои источника из рабочей копии (источник удалён на сервере). */
  dropSource(sourceId: string): void;
  reorder(movedId: string, beforeId: string): void;
  setVisible(id: string, v: boolean): void;
  setLocked(id: string, v: boolean): void;
  rename(id: string, name: string): void;
  // история
  undo(): boolean;
  redo(): boolean;
  /** Сразу записать шаг истории (конец жеста, перед сохранением). */
  flush(): void;
  // layout'ы
  createLayout(name: string): Promise<void>;
  duplicateCurrent(name: string): Promise<void>;
  deleteCurrent(): Promise<void>;
  // снаружи
  setActive(layoutId: string | null): void;
  setObsScenes(scenes: string[]): boolean;
  /** Переименование и прочие изменения свойств layout (имя, canvas, default). */
  metaChanged(): void;
}

export function createEditorModel(deps: EditorDeps): EditorModel {
  const { api } = deps;
  const delay = deps.recordDelayMs ?? 500;
  const store = createStore<EditorState>({
    layouts: [], sources: [], plugins: [], layout: null, selectedId: null, dirty: false, defaultLayoutId: null,
    activeLayoutId: null, obsScenes: [], canUndo: false, canRedo: false, change: "reset", rev: 0,
  });
  let log: History | null = null;
  let timer: ReturnType<typeof setTimeout> | undefined;

  const snapshot = (l: Layout | null = store.get().layout) => JSON.stringify(l ? { canvas: l.canvas, objects: l.objects } : null);

  /** Опубликовать новое состояние: всегда новый объект, чтобы подписчики сработали (рабочая копия меняется на месте). */
  function emit(change: ChangeKind, patch: Partial<EditorState> = {}) {
    const s = store.get();
    store.set({ ...s, ...patch, canUndo: !!log?.canUndo, canRedo: !!log?.canRedo, change, rev: s.rev + 1 });
  }

  function resetHistory(l: Layout | null) {
    clearTimeout(timer);
    log = l ? new History(snapshot(l)) : null;
  }

  function scheduleRecord() {
    clearTimeout(timer);
    timer = setTimeout(flush, delay);
  }

  function flush() {
    clearTimeout(timer);
    if (!log || !store.get().layout) return;
    log.record(snapshot());
    emit("geometry", { dirty: log.dirty });
  }

  function dirtyChange(change: ChangeKind, patch: Partial<EditorState> = {}) {
    scheduleRecord();
    emit(change, { ...patch, dirty: true });
  }

  const layoutOrNull = () => store.get().layout;
  const find = (id: string) => layoutOrNull()?.objects.find((o) => o.id === id);

  async function loadSources() { emit("data", { sources: await api<Source[]>("/api/sources") }); }
  async function loadPlugins() { emit("data", { plugins: await api<PluginManifest[]>("/api/plugins") }); }
  async function loadLayouts() {
    const [layouts, def] = await Promise.all([api<Layout[]>("/api/layouts"), api<{ layoutId: string | null }>("/api/default-layout")]);
    emit("meta", { layouts, defaultLayoutId: def.layoutId });
  }

  async function openLayout(id: string, force = false) {
    if (!force && store.get().dirty && !deps.confirmDiscard()) { emit("meta"); return false; } // вернуть select на текущий layout
    const layout = await api<Layout>(`/api/layouts/${encodeURIComponent(id)}`);
    resetHistory(layout);
    emit("reset", { layout, selectedId: null, dirty: false });
    return true;
  }

  const model: EditorModel = {
    get: store.get,
    subscribe: store.subscribe,
    loadSources, loadPlugins, loadLayouts, openLayout,

    async bootstrap() {
      await Promise.all([loadSources(), loadLayouts(), loadPlugins()]);
      if (store.get().layouts.length === 0) {
        const l = await api<Layout>("/api/layouts", "POST", { name: "Layout 1" });
        emit("meta", { layouts: [l] });
      }
      const st = await api<{ activeLayoutId: string | null; scene: string | null; obs: ObsInfo }>("/api/state");
      emit("meta", { activeLayoutId: st.activeLayoutId });
      const { layouts } = store.get();
      await openLayout(st.activeLayoutId && layouts.some((l) => l.id === st.activeLayoutId) ? st.activeLayoutId : layouts[0].id, true);
      return st.obs;
    },

    async reloadCurrent() {
      const cur = layoutOrNull();
      if (!cur || store.get().dirty) return;
      const layout = await api<Layout>(`/api/layouts/${encodeURIComponent(cur.id)}`);
      let selectedId = store.get().selectedId;
      if (selectedId && !layout.objects.some((o) => o.id === selectedId)) selectedId = null;
      // чужое/серверное состояние — отдельный шаг истории и новая «сохранённая» точка
      if (!log) resetHistory(layout); else { clearTimeout(timer); log.record(snapshot(layout)); log.markSaved(); }
      emit("reset", { layout, selectedId, dirty: false });
    },

    async save() {
      const cur = layoutOrNull();
      if (!cur) return;
      flush(); // отправляем и запоминаем ровно то же состояние
      await api(`/api/layouts/${encodeURIComponent(cur.id)}`, "PUT", cur);
      log?.markSaved();
      emit("meta", { dirty: false });
      await loadLayouts();
    },

    select(id) { emit("selection", { selectedId: id }); },
    touch() { dirtyChange("geometry"); },
    markDirty(change = "geometry") { dirtyChange(change); },

    addObject(sourceId, w, h) {
      const l = layoutOrNull();
      if (!l) return null;
      const o = newObject(l, sourceId, store.get().sources.find((s) => s.id === sourceId)?.name, w, h);
      l.objects.push(o);
      dirtyChange("structure", { selectedId: o.id });
      return o;
    },
    insertObject(o) {
      const l = layoutOrNull();
      if (!l) return;
      l.objects.push(o);
      dirtyChange("structure", { selectedId: o.id });
    },
    removeObject(id) {
      const l = layoutOrNull();
      if (!l) return;
      l.objects = l.objects.filter((o) => o.id !== id);
      dirtyChange("structure", { selectedId: store.get().selectedId === id ? null : store.get().selectedId });
    },
    dropSource(sourceId) {
      const l = layoutOrNull();
      if (!l) return;
      const wasDirty = store.get().dirty;
      l.objects = l.objects.filter((x) => x.sourceId !== sourceId);
      // сервер уже вычистил объекты в сохранённых layout — это не правка пользователя: «сохранённая» точка сдвигается вместе
      clearTimeout(timer);
      if (log) { log.record(snapshot()); if (!wasDirty) log.markSaved(); }
      emit("reset", { selectedId: null, dirty: wasDirty });
    },
    reorder(movedId, beforeId) {
      const l = layoutOrNull();
      if (l && reorderObjects(l, movedId, beforeId)) dirtyChange("structure");
    },
    setVisible(id, v) { const o = find(id); if (o) { o.visible = v; dirtyChange("structure"); } },
    setLocked(id, v) { const o = find(id); if (o) { o.locked = v; dirtyChange("layers"); } },
    rename(id, name) { const o = find(id); if (o) { o.name = name; dirtyChange("layers"); } },

    undo() {
      flush();
      if (!log?.canUndo) return false;
      apply(log.undo());
      return true;
    },
    redo() {
      flush();
      if (!log?.canRedo) return false;
      apply(log.redo());
      return true;
    },
    flush,

    async createLayout(name) {
      const l = await api<Layout>("/api/layouts", "POST", { name });
      await loadLayouts();
      await openLayout(l.id);
    },
    async duplicateCurrent(name) {
      const cur = layoutOrNull();
      if (!cur) return;
      // дублируем сохранённую версию на сервере
      const l = await api<Layout>("/api/layouts", "POST", { name, duplicateOf: cur.id });
      await loadLayouts();
      await openLayout(l.id, true);
    },
    async deleteCurrent() {
      const cur = layoutOrNull();
      if (!cur || store.get().layouts.length <= 1) return;
      await api(`/api/layouts/${encodeURIComponent(cur.id)}`, "DELETE");
      await loadLayouts();
      await openLayout(store.get().layouts[0].id, true);
    },

    setActive(id) { if (id && id !== store.get().activeLayoutId) emit("meta", { activeLayoutId: id }); else emit("meta"); },
    setObsScenes(scenes) {
      if (JSON.stringify(store.get().obsScenes) === JSON.stringify(scenes)) return false;
      emit("meta", { obsScenes: scenes });
      return true;
    },
    metaChanged() { dirtyChange("meta"); },
  };

  function apply(s: string | null) {
    const l = layoutOrNull();
    if (s === null || !l) return;
    const st = JSON.parse(s) as { canvas: Layout["canvas"]; objects: LayoutObject[] } | null;
    if (!st) return;
    l.canvas = st.canvas;
    l.objects = st.objects;
    const sel = store.get().selectedId;
    emit("reset", { selectedId: sel && l.objects.some((o) => o.id === sel) ? sel : null, dirty: log!.dirty });
  }

  return model;
}
