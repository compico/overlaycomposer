import { el } from "../../lib/dom.ts";
import type { EditorState } from "../editor/model.ts";
import { layoutRows } from "./model.ts";

export interface LayoutsRefs {
  list: HTMLElement;
  select: HTMLSelectElement;
  deleteBtn: HTMLElement;
}

export interface LayoutsPorts {
  open(id: string): void;
  /** Показать layout в /overlay сейчас. */
  activate(id: string): void;
}

/** Список layout'ов и выпадающий выбор в шапке. */
export function mountLayouts(refs: LayoutsRefs, ports: LayoutsPorts) {
  refs.select.addEventListener("change", () => ports.open(refs.select.value));

  return function render(st: Pick<EditorState, "layouts" | "layout" | "defaultLayoutId" | "activeLayoutId">) {
    const currentId = st.layout?.id ?? null;
    const rows = layoutRows(st.layouts, currentId, st.defaultLayoutId, st.activeLayoutId);
    refs.list.replaceChildren();
    for (const r of rows) {
      const item = el("div", { class: `item${r.selected ? " sel" : ""}` });
      const name = el("span", { class: "name", textContent: r.name });
      name.title = r.title;
      item.append(name);
      if (r.isDefault) item.append(el("span", { class: "badge", textContent: "★ default", title: "Shown for scenes that have no layout of their own" }));
      else if (r.sceneCount) item.append(el("span", { class: "badge", textContent: String(r.sceneCount), title: r.sceneList }));
      if (r.live) item.append(el("span", { class: "badge ok", textContent: "live", title: "Shown in /overlay now" }));
      const play = el("button", { class: "ib", textContent: "▶", title: "Show this layout in /overlay now" });
      play.addEventListener("click", (e) => { e.stopPropagation(); ports.activate(r.id); });
      item.append(play);
      item.addEventListener("click", () => { if (!r.selected) ports.open(r.id); });
      refs.list.append(item);
    }
    refs.select.replaceChildren(...st.layouts.map((l) => el("option", { value: l.id, textContent: l.name, selected: l.id === currentId })));
    refs.deleteBtn.toggleAttribute("disabled", st.layouts.length <= 1);
  };
}
