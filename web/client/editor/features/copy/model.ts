// Копирование слоёв: между layout'ами (диалог) и через буфер (Ctrl+C / Ctrl+V). Без DOM.
import type { Layout, LayoutObject, Source } from "../../../../shared/types.ts";
import { uid } from "../../../../shared/types.ts";
import type { Api } from "../../lib/api.ts";
import { layerName } from "../layers/model.ts";

export interface CopyOptions {
  /** Связанная копия: тот же instanceId — виджет не перезагружается при смене сцены. */
  linked: boolean;
  offset?: number;
  copySuffix?: boolean;
}

/** Копия слоя для layout `target`. Связь не создаётся, если такой instanceId в target уже есть. */
export function cloneObject(o: LayoutObject, target: Pick<Layout, "objects">, opts: CopyOptions, sources: Pick<Source, "id" | "name">[] = []): LayoutObject {
  const c = JSON.parse(JSON.stringify(o)) as LayoutObject;
  c.id = uid("obj");
  const canLink = opts.linked && !!o.instanceId && !target.objects.some((x) => x.instanceId === o.instanceId);
  c.instanceId = canLink ? o.instanceId : uid("inst");
  c.zIndex = Math.max(0, ...target.objects.map((x) => x.zIndex)) + 1;
  c.transform.x += opts.offset ?? 0;
  c.transform.y += opts.offset ?? 0;
  if (opts.copySuffix) c.name = `${layerName(o, sources)} (copy)`;
  return c;
}

/** Что копированию нужно от редактора (подмножество EditorModel). */
export interface CopyEditorPort {
  get(): { layout: Layout | null; layouts: Layout[]; sources: Source[]; selectedId: string | null; dirty: boolean };
  markDirty(): void;
  save(): Promise<boolean>;
  insertObject(o: LayoutObject): void;
  loadLayouts(): Promise<void>;
}

export interface CopyDeps {
  api: Api;
  editor: CopyEditorPort;
  toast(msg: string): void;
}

export interface CopyModel {
  hasClip(): boolean;
  /** Слои других layout'ов, куда можно копировать; пусто — копировать некуда. */
  targets(): Layout[];
  /** Выбранный слой: для подписи диалога. */
  selection(): { name: string; layoutName: string } | null;
  /** Скопировать в буфер выбранный слой (Ctrl+C). */
  copySelected(): Promise<void>;
  /** Вставить из буфера в открытый layout (Ctrl+V). */
  paste(): void;
  /** Скопировать выбранный слой в другой layout (диалог). */
  copyToLayout(targetId: string, linked: boolean): Promise<void>;
}

export function createCopyModel(deps: CopyDeps): CopyModel {
  const { api, editor, toast } = deps;
  let clip: { obj: LayoutObject; fromLayoutId: string } | null = null;

  function selectedObj(): LayoutObject | null {
    const st = editor.get();
    return st.layout?.objects.find((o) => o.id === st.selectedId) ?? null;
  }

  /**
   * Гарантирует у слоя instanceId (нужен для связи копий). Если он назначен сейчас, а несохранённых правок не было —
   * сразу сохраняем layout, чтобы связь работала; иначе предупреждаем, что layout надо сохранить.
   */
  async function ensureInstanceId(o: LayoutObject) {
    if (o.instanceId) return;
    const wasDirty = editor.get().dirty;
    o.instanceId = uid("inst");
    editor.markDirty();
    if (wasDirty) toast("Save this layout (Ctrl+S) so the copy stays linked");
    else if (!(await editor.save())) toast("Save this layout (Ctrl+S) so the copy stays linked");
  }

  return {
    hasClip: () => !!clip,
    targets() {
      const st = editor.get();
      return st.layouts.filter((l) => l.id !== st.layout?.id);
    },
    selection() {
      const st = editor.get();
      const o = selectedObj();
      return st.layout && o ? { name: layerName(o, st.sources), layoutName: st.layout.name } : null;
    },
    async copySelected() {
      const st = editor.get();
      const o = selectedObj();
      if (!st.layout || !o) return;
      await ensureInstanceId(o);
      clip = { obj: JSON.parse(JSON.stringify(o)) as LayoutObject, fromLayoutId: st.layout.id };
      toast(`Copied “${layerName(o, st.sources)}” — Ctrl+V pastes it into the open layout`);
    },
    paste() {
      const st = editor.get();
      if (!st.layout || !clip) return;
      const same = clip.fromLayoutId === st.layout.id;
      // В тот же layout — независимая копия со сдвигом; в другой — связанная (тот же виджет).
      editor.insertObject(cloneObject(clip.obj, st.layout, { linked: !same, offset: same ? 20 : 0, copySuffix: same }, st.sources));
    },
    async copyToLayout(targetId, linked) {
      const st = editor.get();
      const o = selectedObj();
      if (!st.layout || !o || !targetId) return;
      if (linked) await ensureInstanceId(o);
      const target = await api<Layout>(`/api/layouts/${encodeURIComponent(targetId)}`);
      target.objects.push(cloneObject(o, target, { linked }, st.sources));
      await api(`/api/layouts/${encodeURIComponent(targetId)}`, "PUT", target);
      await editor.loadLayouts();
      toast(`Copied to “${target.name}”`);
    },
  };
}
