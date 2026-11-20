// Автономные (wasm) плагины в окне магазина (ванильный DOM). Подтверждения и тосты — здесь: это UI, а не модель.
import { el } from "../../lib/dom.ts";
import { PERM_TEXT } from "../../lib/permissions.ts";
import type { Ui } from "../../lib/ui.ts";
import type { WasmEntry, WasmStoreModel, WasmStoreState } from "./wasm.ts";
import { changesBetween, installAction, versionChoices, type WasmChange } from "./wasm-versions.ts";

export interface WasmStorePorts {
  ui: Ui;
  /** Открыть окно «Plugins»: там выдаются права (новые права после обновления не выданы). */
  openPlugins(): void;
}

const permText = (p: string) => PERM_TEXT[p] ?? p;

export function mountWasmStore(box: HTMLElement, model: WasmStoreModel, ports: WasmStorePorts) {
  const { ui } = ports;

  /** Кнопка действия: блокируется на время операции; ошибки — в тост (каталог модель перечитывает сама). */
  const act = (btn: HTMLButtonElement, fn: () => Promise<unknown>) => {
    btn.addEventListener("click", async () => {
      btn.disabled = true;
      try { await fn(); } catch (e) { ui.fail(e); }
    });
    return btn;
  };

  /**
   * Окно обновления: что изменилось во всех пропущенных версиях, что сделать после обновления и какие новые права просит плагин.
   * Resolves true — пользователь согласен обновить.
   */
  function confirmUpdate(p: WasmEntry, to: string, log: WasmChange[], added: string[]): Promise<boolean> {
    return new Promise((resolve) => {
      const dialog = el("dialog", { class: "wasm-update", style: "max-width:560px;width:90vw" });
      const body = el("div", { style: "max-height:60vh;overflow:auto" });
      body.append(el("strong", { textContent: `Update “${p.name}”: ${p.installedVersion} → ${to}` }));
      if (added.length) {
        body.append(el("div", { class: "hint warn", textContent: `The new version asks for additional permissions: ${added.map(permText).join("; ")}. They stay off until you allow them in Plugins.` }));
      }
      if (!log.length) body.append(el("div", { class: "hint", textContent: "The author did not describe this update." }));
      for (const c of log) {
        body.append(el("h4", { textContent: c.date ? `${c.version} — ${c.date}` : c.version }));
        if (c.changes?.length) body.append(el("ul", {}, ...c.changes.map((x) => el("li", { textContent: x }))));
        if (c.migration) body.append(el("div", { class: "hint", textContent: "What to do after updating:" }), el("div", { class: "migration", textContent: c.migration }));
      }
      const ok = el("button", { type: "button", class: "primary", textContent: `Update to ${to}` });
      const cancel = el("button", { type: "button", textContent: "Cancel" });
      let result = false;
      ok.addEventListener("click", () => { result = true; dialog.close(); });
      cancel.addEventListener("click", () => dialog.close());
      dialog.addEventListener("close", () => { dialog.remove(); resolve(result); });
      dialog.append(body, el("div", { class: "row", style: "justify-content:flex-end;gap:6px;margin-top:10px" }, cancel, ok));
      document.body.append(dialog);
      dialog.showModal();
    });
  }

  function entryRow(p: WasmEntry): HTMLElement {
    const choices = versionChoices(p.versions);
    const select = el("select", { title: "Version", style: "max-width:150px" }) as HTMLSelectElement;
    for (const c of choices) select.append(el("option", { value: c.value, textContent: c.label }));
    const current = p.installedTag ? choices.find((c) => c.tag === p.installedTag && c.value === c.version) : undefined;
    if (current) select.value = current.value;

    const button = el("button", { type: "button" }) as HTMLButtonElement;
    const chosen = () => choices.find((c) => c.value === select.value) ?? choices[0];
    const refreshButton = () => {
      const c = chosen();
      if (!c) { button.textContent = "No releases"; button.disabled = true; return; }
      const a = installAction(p.installed ? p.installedVersion : undefined, c.version);
      button.textContent = a.label;
      button.disabled = a.kind === "same";
      button.className = a.kind === "downgrade" ? "danger" : "primary";
    };
    select.addEventListener("change", refreshButton);
    refreshButton();
    act(button, async () => {
      const c = chosen();
      if (!c) return;
      const a = installAction(p.installed ? p.installedVersion : undefined, c.version);
      const latest = c.tag === p.tag;
      if (a.kind === "downgrade" && !confirm(`Switch “${p.name}” to the older ${c.version}?\n\nA newer version may have changed the plugin's saved data; the old one might not understand it.`)) {
        await model.load();
        return;
      }
      if (a.kind === "update" && p.installedVersion) {
        const log = changesBetween(p.changelog, p.installedVersion, c.version);
        const added = latest ? p.addedPermissions ?? [] : [];
        if ((log.length || added.length) && !(await confirmUpdate(p, c.version, log, added))) {
          await model.load();
          return;
        }
      }
      const res = await model.install({ repo: p.repo, id: p.id, tag: c.tag, ...(latest && p.sha256 ? { sha256: p.sha256 } : {}) });
      ui.toast(`${p.name} ${res.version} ${res.previous ? "installed (was " + res.previous + ")" : "installed"}.`);
      if (res.addedPermissions?.length) {
        ui.toast(`${p.name} asks for new permissions — allow them in Plugins.`);
        ports.openPlugins();
      }
    });

    const controls: HTMLElement[] = [];
    if (p.blocked) controls.push(el("span", { class: "hint", textContent: "id is taken by a plugin not installed from the store" }));
    else {
      controls.push(select, button);
      if (p.installed) {
        controls.push(act(el("button", { type: "button", class: "danger", textContent: "Uninstall" }), async () => {
          if (!confirm(`Uninstall “${p.name}”? Its permissions and settings are kept.`)) { await model.load(); return; }
          await model.uninstall(p.id);
        }));
      }
    }

    const perms = p.noInfo ? "No metadata in this release (older release): permissions are shown after install"
      : p.permissions?.length ? `Asks for: ${p.permissions.map(permText).join("; ")}` : "Asks for no permissions";
    const state = p.installed ? ` — installed ${p.installedVersion}${p.updateAvailable ? " (update available)" : ""}` : "";
    return el("div", { class: "row", style: "align-items:flex-start;justify-content:space-between;border-top:1px solid var(--line);padding-top:6px" },
      el("div", { style: "flex:1;min-width:0" },
        el("div", { textContent: `${p.name} ${p.version}${state}` }),
        el("div", { class: "hint", textContent: [p.author, p.description].filter(Boolean).join(" — ") }),
        el("div", { class: "hint", textContent: perms })),
      el("div", { class: "row", style: "gap:6px;flex-wrap:wrap;justify-content:flex-end" }, ...controls));
  }

  function render(s: WasmStoreState) {
    if (s.loading && !s.sources.length) { box.replaceChildren(el("div", { class: "empty", textContent: "Loading…" })); return; }
    if (s.error) { box.replaceChildren(el("div", { class: "hint warn", textContent: `Standalone plugins are unavailable: ${s.error}` })); return; }
    const parts: HTMLElement[] = [];
    for (const src of s.sources) {
      if (!src.error && !src.plugins.length) continue; // в этом источнике автономных плагинов нет
      parts.push(el("div", { style: "margin-top:12px" }, el("strong", { textContent: `${src.repo} — standalone plugins` })));
      if (src.error) parts.push(el("div", { class: "hint warn", textContent: src.error }));
      for (const p of src.plugins) parts.push(entryRow(p));
    }
    box.replaceChildren(...parts);
  }
  model.subscribe(render);
  return { load: (refresh = false) => model.load(refresh) };
}
