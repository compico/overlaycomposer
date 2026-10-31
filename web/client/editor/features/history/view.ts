import type { HistoryEntry } from "../../../../shared/history.ts";
import { el } from "../../lib/dom.ts";
import type { Readable } from "../../lib/store.ts";
import { historyRows, historySummary } from "./model.ts";

export interface HistoryRefs {
  btn: HTMLElement;
  panel: HTMLElement;
}

/** Что панели нужно от редактора (подмножество EditorModel). */
export interface HistoryPorts {
  jumpTo(index: number): boolean;
}

/** Выпадающий список шагов истории: клик переносит на выбранный шаг (назад или вперёд). */
export function mountHistory(refs: HistoryRefs, state: Readable<{ history: HistoryEntry[] }>, ports: HistoryPorts) {
  const open = () => refs.panel.matches(":popover-open");

  function render() {
    const entries = state.get().history;
    refs.btn.title = historySummary(entries);
    if (!open()) return;
    const rows = historyRows(entries);
    refs.panel.replaceChildren(
      el("div", { class: "hint", style: "padding:2px 6px 6px" }, "Click a step to jump to it. Steps in grey are redo; a new edit discards them."),
      ...rows.map((r) => {
        const b = el("button", {
          type: "button",
          class: `hist${r.current ? " cur" : ""}${r.future ? " future" : ""}`,
          title: r.hint,
        }, el("span", { class: "name" }, r.label), r.saved ? el("span", { class: "mark", title: "Matches the saved version" }, "●") : "");
        b.disabled = !r.canJump && !r.current;
        b.addEventListener("click", () => { if (r.canJump) ports.jumpTo(r.index); });
        return b;
      }),
    );
  }

  let last: HistoryEntry[] | null = null;
  state.subscribe((s) => {
    if (s.history === last) return; // история не менялась (двигаются только поля)
    last = s.history;
    render();
  });

  refs.btn.addEventListener("click", () => {
    if (open()) { refs.panel.hidePopover(); return; }
    const r = refs.btn.getBoundingClientRect();
    refs.panel.style.top = `${r.bottom + 6}px`;
    refs.panel.style.left = `${Math.max(8, Math.min(r.left, window.innerWidth - 308))}px`;
    refs.panel.showPopover();
    render();
  });
}
