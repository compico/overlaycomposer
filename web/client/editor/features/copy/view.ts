import { el } from "../../lib/dom.ts";
import type { CopyModel } from "./model.ts";

export interface CopyRefs {
  dialog: HTMLDialogElement;
  what: HTMLElement;
  target: HTMLSelectElement;
  linked: HTMLInputElement;
  openBtn: HTMLElement;
  goBtn: HTMLElement;
}

export interface CopyPorts {
  toast(msg: string): void;
  fail(e: unknown): void;
}

/** Диалог «скопировать слой в другой layout». */
export function mountCopy(refs: CopyRefs, model: CopyModel, ports: CopyPorts) {
  refs.openBtn.addEventListener("click", () => {
    const sel = model.selection();
    if (!sel) return ports.toast("Select a layer first");
    const targets = model.targets();
    if (targets.length === 0) return ports.toast("Create another layout first");
    refs.what.textContent = `Layer “${sel.name}” from “${sel.layoutName}”`;
    refs.target.replaceChildren(...targets.map((l) => el("option", { value: l.id, textContent: l.name })));
    refs.dialog.showModal();
  });

  refs.goBtn.addEventListener("click", async () => {
    const targetId = refs.target.value;
    const linked = refs.linked.checked;
    if (!targetId) return;
    refs.dialog.close();
    try { await model.copyToLayout(targetId, linked); } catch (e) { ports.fail(e); }
  });
}
