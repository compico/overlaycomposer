import { el } from "../../lib/dom.ts";
import type { EditorState } from "../editor/model.ts";
import { layerRows } from "./model.ts";

/** Что панели слоёв нужно от редактора (подмножество EditorModel). */
export interface LayersPorts {
  select(id: string | null): void;
  setVisible(id: string, v: boolean): void;
  setLocked(id: string, v: boolean): void;
  rename(id: string, name: string): void;
  removeObject(id: string): void;
  reorder(movedId: string, beforeId: string): void;
  /** Запрос имени для переименования (prompt в браузере). */
  askName(current: string): string | null;
}

/** Перерисовать список слоёв по состоянию редактора. */
export function mountLayers(list: HTMLElement, ports: LayersPorts) {
  let dragId: string | null = null;

  return function render(st: Pick<EditorState, "layout" | "sources" | "selectedId">) {
    list.replaceChildren();
    if (!st.layout) return;
    const rows = layerRows(st.layout.objects, st.sources, st.selectedId);
    if (rows.length === 0) list.append(el("div", { class: "empty", textContent: "No objects. Click “+ Add Source”." }));

    for (const r of rows) {
      const item = el("div", { class: `item${r.selected ? " sel" : ""}${r.visible ? "" : " hidden"}` });
      item.draggable = true;

      const eye = el("button", { class: "ib", textContent: r.visible ? "👁" : "◌", title: "Visibility" });
      eye.addEventListener("click", (e) => { e.stopPropagation(); ports.setVisible(r.id, !r.visible); });
      const lock = el("button", { class: "ib", textContent: r.locked ? "🔒" : "🔓", title: "Lock" });
      lock.addEventListener("click", (e) => { e.stopPropagation(); ports.setLocked(r.id, !r.locked); });
      const name = el("span", { class: "name", textContent: r.name, title: "Double-click to rename this layer (the source keeps its name)" });
      name.addEventListener("dblclick", (e) => {
        e.stopPropagation();
        const n = ports.askName(r.name);
        if (n === null || !n.trim() || n.trim() === r.name) return;
        ports.rename(r.id, n.trim());
      });
      const del = el("button", { class: "ib", textContent: "✕", title: "Remove from layout" });
      del.addEventListener("click", (e) => { e.stopPropagation(); ports.removeObject(r.id); });

      item.append(eye, lock, name, del);
      item.addEventListener("click", () => ports.select(r.id));

      item.addEventListener("dragstart", (e) => {
        dragId = r.id;
        e.dataTransfer?.setData("text/plain", r.id);
      });
      item.addEventListener("dragover", (e) => {
        if (!dragId) return;
        e.preventDefault();
        item.classList.add("drop-before");
      });
      item.addEventListener("dragleave", () => item.classList.remove("drop-before"));
      item.addEventListener("drop", (e) => {
        e.preventDefault();
        item.classList.remove("drop-before");
        if (dragId && dragId !== r.id) ports.reorder(dragId, r.id);
        dragId = null;
      });
      item.addEventListener("dragend", () => { dragId = null; });
      list.append(item);
    }
  };
}
