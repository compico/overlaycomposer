import { el } from "../../lib/dom.ts";
import type { Ui } from "../../lib/ui.ts";
import {
  type BundleModel, type BundleItem, type PlanResponse, type Selection, type SectionPlan,
  defaultSelection, isCopyable, reportLines, sectionTitle, SECTIONS, selectable, selectedCount, STATUS_LABEL,
} from "./model.ts";

export interface BundleRefs {
  /** Список чекбоксов секций для экспорта. */
  exportBox: HTMLElement;
  exportBtn: HTMLButtonElement;
  importBtn: HTMLButtonElement;
  /** Скрытый <input type=file>. */
  file: HTMLInputElement;
}

export interface BundlePorts {
  ui: Ui;
  /** После применения: перечитать sources и layouts, обновить интерфейс. */
  afterImport(): Promise<void>;
}

const DANGER = "color:var(--danger,#e5484d)";
const WARN = "color:var(--warn,#d19a00)";

export function mountSettingsBundle(refs: BundleRefs, model: BundleModel, ports: BundlePorts) {
  // ---- экспорт ----
  const boxes = SECTIONS.map((s) => {
    const box = el("input", { type: "checkbox", checked: !s.optional }) as HTMLInputElement;
    box.addEventListener("change", updateLink);
    refs.exportBox.append(
      el("label", { class: "chk" }, box, ` ${s.title}`, ...(s.hint ? [el("span", { class: "hint", textContent: ` — ${s.hint}` })] : [])),
    );
    return { name: s.name, box };
  });
  const chosen = () => boxes.filter((b) => b.box.checked).map((b) => b.name);
  function updateLink() {
    refs.exportBtn.disabled = chosen().length === 0;
  }
  updateLink();
  refs.exportBtn.addEventListener("click", () => {
    // обычная загрузка по ссылке: файл приходит с сервера как есть (контрольные суммы считаются по его байтам)
    const a = el("a", { href: model.exportUrl(chosen()), download: "oc-settings.json" });
    a.click();
  });

  // ---- импорт ----
  refs.importBtn.addEventListener("click", () => refs.file.click());
  refs.file.addEventListener("change", async () => {
    const f = refs.file.files?.[0];
    refs.file.value = "";
    if (!f) return;
    try {
      const text = await f.text();
      const plan = await model.plan(text);
      openImportDialog(text, plan);
    } catch (e) { ports.ui.fail(e); }
  });

  function itemRow(sec: SectionPlan, item: BundleItem, sel: Selection, refresh: () => void): HTMLElement {
    const picked = sel[sec.name]?.[item.id];
    const box = el("input", { type: "checkbox", checked: picked !== undefined, disabled: !selectable(item) }) as HTMLInputElement;
    const mode = el("select", { hidden: true }, el("option", { value: "replace", textContent: "Replace" }), el("option", { value: "copy", textContent: "Import as a copy" })) as HTMLSelectElement;
    const syncMode = () => {
      mode.hidden = !(box.checked && item.status === "replace" && isCopyable(sec.name));
      if (box.checked) (sel[sec.name] ??= {})[item.id] = mode.value as "replace" | "copy";
    };
    box.addEventListener("change", () => {
      if (box.checked) (sel[sec.name] ??= {})[item.id] = mode.value as "replace" | "copy";
      else if (sel[sec.name]) delete sel[sec.name][item.id];
      syncMode();
      refresh();
    });
    mode.addEventListener("change", syncMode);
    syncMode();
    const color = item.status === "problem" ? DANGER : item.warning ? WARN : "";
    const lines: HTMLElement[] = [
      el("div", { textContent: `${item.name || item.id} — ${STATUS_LABEL[item.status]}`, style: item.status === "problem" ? DANGER : "" }),
    ];
    if (item.reason) lines.push(el("div", { class: "hint", textContent: item.reason, style: color }));
    return el("label", { class: "row", style: "align-items:flex-start;gap:8px;border-top:1px solid var(--line);padding:6px 0" },
      box, el("div", { style: "flex:1;min-width:0" }, ...lines), mode);
  }

  /** Переходная модалка: что именно импортировать. Ничего не применяется, пока пользователь не нажмёт «Import». */
  function openImportDialog(text: string, plan: PlanResponse) {
    const dialog = el("dialog", { class: "bundle-import", style: "max-width:680px;width:92vw" });
    const body = el("div", { style: "max-height:62vh;overflow:auto" });
    const sel = defaultSelection(plan.sections);
    const go = el("button", { type: "button", class: "primary" }) as HTMLButtonElement;
    const close = el("button", { type: "button", textContent: "Cancel" }) as HTMLButtonElement;
    const refresh = () => {
      const n = selectedCount(sel);
      go.textContent = n ? `Import ${n} selected` : "Nothing selected";
      go.disabled = n === 0;
    };
    body.append(
      el("strong", { textContent: "Import settings" }),
      el("div", { class: "hint", textContent: `File made ${plan.createdAt}${plan.app ? ` by Overlay Composer ${plan.app}` : ""}. Nothing is changed until you press Import; your current settings are backed up first.` }),
      el("div", { class: "hint warn", textContent: "Secrets (Twitch accounts, passwords, tokens) are never in the file: enter them again where needed." }),
    );
    const sections = plan.sections.slice().sort((a, b) => order(a.name) - order(b.name));
    if (!sections.length) body.append(el("div", { class: "hint", textContent: "The file has no sections." }));
    for (const sec of sections) {
      const head = el("h4", { textContent: sectionTitle(sec.name), style: "margin:12px 0 2px" });
      body.append(head);
      if (!sec.known) { body.append(el("div", { class: "hint", textContent: "This version of Overlay Composer does not know this section — skipped.", style: WARN })); continue; }
      if (sec.error) { body.append(el("div", { class: "hint", textContent: sec.error, style: DANGER })); continue; }
      if (!sec.items?.length) { body.append(el("div", { class: "hint", textContent: "Nothing to import." })); continue; }
      for (const it of sec.items) body.append(itemRow(sec, it, sel, refresh));
    }
    const result = el("div", { class: "hint", style: "white-space:pre-wrap;margin-top:8px" });
    close.addEventListener("click", () => dialog.close());
    go.addEventListener("click", async () => {
      go.disabled = close.disabled = true;
      try {
        const reports = await model.apply(text, sel);
        const lines = reportLines(reports);
        result.textContent = ["Done.", ...lines].join("\n");
        go.hidden = true;
        close.textContent = "Close";
        await ports.afterImport();
        ports.ui.toast("Settings imported");
      } catch (e) { ports.ui.fail(e); go.disabled = false; } finally { close.disabled = false; }
    });
    dialog.addEventListener("close", () => dialog.remove());
    dialog.append(body, result, el("div", { class: "row", style: "justify-content:flex-end;gap:6px;margin-top:10px" }, close, go));
    refresh();
    document.body.append(dialog);
    dialog.showModal();
  }

  return { openImportDialog };
}

const order = (name: string): number => {
  const i = SECTIONS.findIndex((s) => s.name === name);
  return i < 0 ? SECTIONS.length : i;
};
