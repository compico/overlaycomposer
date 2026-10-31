import type { PluginManifest, PluginSetting, Source } from "../../../../shared/types.ts";
import { el } from "../../lib/dom.ts";
import { PERM_TEXT } from "../../lib/permissions.ts";
import { isHexColor, type PluginSettingsModel, type SettingValue, storageText } from "./model.ts";

export interface PluginSettingsPorts {
  fail(e: unknown): void;
  /** Не удалось сохранить: панель свойств перерисовывается, чтобы показать настоящие значения. */
  rollback(): void;
  confirm(text: string): boolean;
}

const COMMON_FONTS = ["Segoe UI", "Arial", "Verdana", "Tahoma", "Trebuchet MS", "Georgia", "Times New Roman", "Courier New", "Consolas",
  "Impact", "Comic Sans MS", "Roboto", "Open Sans", "Montserrat", "Noto Sans", "system-ui"];

function ensureFontList() {
  if (document.getElementById("fontList")) return;
  document.body.append(el("datalist", { id: "fontList" }, ...COMMON_FONTS.map((f) => el("option", { value: f }))));
}

/** Форма настроек плагина-виджета по схеме. Каждое изменение сохраняется сразу (виджет перезагрузится с новыми query). */
export function mountPluginSettings(model: PluginSettingsModel, ports: PluginSettingsPorts) {
  ensureFontList();

  const save = async (src: Source, key: string, v: SettingValue | undefined) => {
    try { await model.saveSetting(src, key, v); } catch (e) { ports.fail(e); ports.rollback(); }
  };

  /** Сколько данных плагин хранит (oc.storage) и кнопка «стереть» — данные общие для всех виджетов этого плагина. */
  function dataRow(pluginId: string): HTMLElement {
    const info = el("span", { class: "hint", textContent: "Plugin data: …" });
    const erase = el("button", { type: "button", textContent: "Erase data" });
    const refresh = async () => {
      try {
        const st = await model.storage(pluginId);
        info.textContent = storageText(st);
        erase.toggleAttribute("disabled", st.keys === 0);
      } catch { info.textContent = "Plugin data: unavailable"; }
    };
    erase.addEventListener("click", async () => {
      if (!ports.confirm("Erase all data stored by this plugin? This cannot be undone.")) return;
      try { await model.eraseStorage(pluginId); await refresh(); } catch (e) { ports.fail(e); }
    });
    void refresh();
    return el("div", { class: "row", style: "justify-content:space-between" }, info, erase);
  }

  function control(src: Source, s: PluginSetting): HTMLElement {
    const current = src.data.settings?.[s.key];
    const val = current ?? s.default;
    const line = el("div", { class: "line" });
    let input: HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement;

    switch (s.type) {
      case "bool": {
        const chk = el("input", { type: "checkbox", checked: Boolean(val) });
        chk.addEventListener("change", () => save(src, s.key, chk.checked));
        input = chk;
        break;
      }
      case "number":
      case "range": {
        const num = el("input", { type: s.type === "range" ? "range" : "number" });
        if (s.min !== undefined) num.min = String(s.min);
        if (s.max !== undefined) num.max = String(s.max);
        num.step = String(s.step ?? 1);
        num.value = String(val ?? s.min ?? 0);
        const shown = el("span", { textContent: s.type === "range" ? String(num.value) : "" });
        num.addEventListener("input", () => (shown.textContent = s.type === "range" ? num.value : ""));
        num.addEventListener("change", () => {
          const v = parseFloat(num.value);
          if (!Number.isNaN(v)) save(src, s.key, v);
        });
        input = num;
        if (s.type === "range") line.append(num, shown);
        break;
      }
      case "color": {
        const col = el("input", { type: "color", value: isHexColor(val) ? val : "#000000" });
        col.addEventListener("change", () => save(src, s.key, col.value));
        input = col;
        break;
      }
      case "select": {
        const sel = el("select", {}, ...(s.options ?? []).map((o) => el("option", { value: o.value, textContent: o.label, selected: o.value === val })));
        sel.addEventListener("change", () => save(src, s.key, sel.value));
        input = sel;
        break;
      }
      case "text": {
        const ta = el("textarea", { value: String(val ?? ""), rows: 3 });
        ta.addEventListener("change", () => save(src, s.key, ta.value));
        input = ta;
        break;
      }
      default: { // string, font
        const txt = el("input", { type: "text", value: String(val ?? "") });
        if (s.type === "font") txt.setAttribute("list", "fontList");
        txt.addEventListener("change", () => save(src, s.key, txt.value));
        input = txt;
      }
    }

    const isBool = s.type === "bool";
    if (s.type !== "range") line.append(input);
    const wrap = el("div", { class: "setting" });
    if (isBool) {
      wrap.append(el("div", { class: "line" }, el("label", { class: "chk", style: "margin:0" }, input, s.label)));
    } else {
      wrap.append(el("span", { textContent: s.label }), line);
    }
    if (current !== undefined) { // значение отличается от умолчания → можно сбросить
      const reset = el("button", { class: "ib", textContent: "↺", title: "Reset to default" });
      reset.addEventListener("click", () => save(src, s.key, undefined));
      (isBool ? wrap.firstElementChild! : line).append(reset);
    }
    if (s.help) wrap.append(el("div", { class: "hint", textContent: s.help }));
    return wrap;
  }

  return function render(src: Source, m: PluginManifest): HTMLElement {
    const root = el("div");
    if (m.permissions?.length) {
      root.append(el("h4", { class: "group", textContent: "Permissions" }));
      for (const perm of m.permissions) {
        const chk = el("input", { type: "checkbox", checked: src.data.granted?.includes(perm) ?? false });
        chk.addEventListener("change", async () => {
          try { await model.setGranted(src, perm, chk.checked); } catch (e) { ports.fail(e); ports.rollback(); }
        });
        root.append(el("label", { class: "chk" }, chk, ` ${PERM_TEXT[perm] ?? perm}`));
      }
      root.append(el("div", { class: "hint", textContent: "Changing permissions reloads the widget." }));
      if (m.permissions.includes("storage")) root.append(dataRow(m.id));
    }

    let group: string | undefined;
    for (const s of m.settings ?? []) {
      if (s.group !== group) {
        group = s.group;
        if (group) root.append(el("h4", { class: "group", textContent: group }));
      }
      root.append(control(src, s));
    }
    if (!m.settings?.length) root.append(el("div", { class: "empty", textContent: "This widget has no settings." }));
    return root;
  };
}
