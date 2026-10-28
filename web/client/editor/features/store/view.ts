// Представление магазина плагинов (ванильный DOM). Подтверждения (confirm) и тосты — здесь: это UI, а не модель.
import { el } from "../../lib/dom.ts";
import { PERM_TEXT } from "../../lib/permissions.ts";
import type { Ui } from "../../lib/ui.ts";
import type { StoreData, StoreModel } from "./model.ts";

export interface StoreRefs {
  openBtn: HTMLElement;
  dialog: HTMLDialogElement;
  body: HTMLElement;
  refreshBtn: HTMLElement;
  repoInput: HTMLInputElement;
  addBtn: HTMLButtonElement;
  tokenSummary: HTMLElement;
  tokenInput: HTMLInputElement;
  tokenSave: HTMLElement;
  tokenClear: HTMLButtonElement;
  openPluginsBtn: HTMLElement;
}

export interface StorePorts {
  ui: Ui;
  /** Сколько виджетов используют плагин (для предупреждения при удалении). */
  usedBy(pluginId: string): number;
  /** Имя установленного плагина по id (для «осиротевших» записей). */
  pluginName(id: string): string | undefined;
  openPluginsFolder(): Promise<unknown>;
}

const BLOCKED_TEXT = { builtin: "built in", foreign: "id is taken by another plugin" };

export function mountStore(refs: StoreRefs, model: StoreModel, ports: StorePorts) {
  const { ui } = ports;

  /** Кнопка действия: блокируется на время операции; ошибки — в тост (каталог модель перечитывает сама). */
  const act = (btn: HTMLButtonElement, fn: () => Promise<unknown>) => {
    btn.addEventListener("click", async () => {
      btn.disabled = true;
      try { await fn(); } catch (e) { ui.fail(e); }
    });
    return btn;
  };

  function render(d: StoreData) {
    refs.tokenSummary.textContent = d.hasToken ? "Private repositories — GitHub token is set" : "Private repositories (GitHub token)";
    refs.tokenClear.toggleAttribute("disabled", !d.hasToken);
    const shown = new Set<string>();
    const parts: HTMLElement[] = [];
    for (const s of d.sources) {
      const rm = act(el("button", { type: "button", textContent: "Remove source", title: "Plugins already installed from it stay" }), async () => {
        if (confirm(`Remove source ${s.repo}? Plugins already installed from it stay installed.`)) await model.removeSource(s.repo);
        else await model.load();
      });
      parts.push(el("div", { class: "row", style: "justify-content:space-between;margin-top:12px" }, el("strong", { textContent: s.repo }), rm));
      if (s.error) parts.push(el("div", { class: "hint warn", textContent: s.error }));
      else if (!s.plugins.length) parts.push(el("div", { class: "empty", textContent: "No plugins in this repository." }));
      for (const p of s.plugins) {
        if (p.installed) shown.add(p.id);
        const used = ports.usedBy(p.id);
        const perms = p.permissions?.length ? `Asks for: ${p.permissions.map((x) => PERM_TEXT[x] ?? x).join("; ")}` : "Asks for no permissions";
        let button: HTMLElement;
        if (p.blocked) button = el("span", { class: "hint", textContent: BLOCKED_TEXT[p.blocked] });
        else if (p.updateAvailable) button = act(el("button", { type: "button", class: "primary", textContent: p.version && p.version !== p.installedVersion ? `Update to ${p.version}` : "Update (files changed)" }), async () => {
          // Новые права при обновлении — отдельное подтверждение: молча расширить доступ плагина нельзя.
          if (p.addedPermissions?.length && !confirm(`The new version of “${p.name}” asks for additional permissions:\n\n${p.addedPermissions.map((x) => `• ${PERM_TEXT[x] ?? x}`).join("\n")}\n\nUpdate anyway? (Widgets keep only the permissions you already allowed; allow the new ones in the widget properties.)`)) {
            await model.load();
            return;
          }
          await model.install(p);
          ui.toast(`${p.name} updated. Reload the overlay page in OBS to apply.`);
        });
        else if (p.installed) button = act(el("button", { type: "button", class: "danger", textContent: "Uninstall" }), async () => {
          const msg = used ? `Uninstall “${p.name}”? ${used} widget(s) using it will become empty. Its stored data is kept (erase it in widget properties).` : `Uninstall “${p.name}”?`;
          if (!confirm(msg)) { await model.load(); return; }
          await model.uninstall(p.id);
        });
        else button = act(el("button", { type: "button", class: "primary", textContent: "Install" }), async () => {
          await model.install(p);
          ui.toast(`${p.name} installed. Add it via Add Source → widget plugin.`);
        });
        const src = p.commit ? ` · commit ${p.commit}` : "";
        const title = `${p.name}${p.version ? ` ${p.version}` : ""}${src}${p.installed && p.installedVersion && p.installedVersion !== p.version ? ` (installed ${p.installedVersion})` : p.installed ? " — installed" : ""}`;
        parts.push(el("div", { class: "row", style: "align-items:flex-start;justify-content:space-between;border-top:1px solid var(--line);padding-top:6px" },
          el("div", { style: "flex:1;min-width:0" },
            el("div", { textContent: title }),
            el("div", { class: "hint", textContent: [p.author, p.description].filter(Boolean).join(" — ") }),
            el("div", { class: "hint", textContent: perms })),
          button));
      }
    }
    // Поставленные из магазина, но источник уже убран: управлять ими всё равно надо.
    const orphans = Object.entries(d.installed).filter(([id]) => !shown.has(id));
    if (orphans.length) {
      parts.push(el("div", { style: "margin-top:12px" }, el("strong", { textContent: "Installed from sources no longer in the list" })));
      for (const [id, rec] of orphans) {
        const name = ports.pluginName(id) ?? id;
        const b = act(el("button", { type: "button", class: "danger", textContent: "Uninstall" }), async () => {
          if (!confirm(`Uninstall “${name}”?`)) { await model.load(); return; }
          await model.uninstall(id);
        });
        parts.push(el("div", { class: "row", style: "justify-content:space-between;border-top:1px solid var(--line);padding-top:6px" },
          el("div", {}, el("div", { textContent: `${name}${rec.version ? ` ${rec.version}` : ""}` }), el("div", { class: "hint", textContent: rec.repo })), b));
      }
    }
    if (!d.sources.length && !orphans.length) parts.push(el("div", { class: "empty", textContent: "No sources yet. Add a GitHub repository above." }));
    refs.body.replaceChildren(...parts);
  }

  model.subscribe((s) => {
    if (s.loading) refs.body.replaceChildren(el("div", { class: "empty", textContent: "Loading…" }));
    else if (s.error) refs.body.replaceChildren(el("div", { class: "hint warn", textContent: `Store is unavailable: ${s.error}` }));
    else if (s.data) render(s.data);
  });

  /** Открыть окно магазина (и из других мест, например из уведомления об обновлении плагина). */
  const open = () => { refs.dialog.showModal(); void model.load(); };
  refs.openBtn.addEventListener("click", open);
  refs.refreshBtn.addEventListener("click", () => void model.load(true));
  refs.addBtn.addEventListener("click", async () => {
    if (!refs.repoInput.value.trim()) return;
    refs.addBtn.disabled = true;
    try { await model.addSource(refs.repoInput.value); refs.repoInput.value = ""; } catch (e) { ui.fail(e); }
    refs.addBtn.disabled = false;
  });
  refs.tokenSave.addEventListener("click", async () => {
    if (!refs.tokenInput.value.trim()) return;
    try { await model.saveToken(refs.tokenInput.value); refs.tokenInput.value = ""; ui.toast("GitHub token saved"); } catch (e) { ui.fail(e); }
  });
  refs.tokenClear.addEventListener("click", async () => {
    if (!confirm("Remove the saved GitHub token? Private repositories will stop loading.")) return;
    try { await model.clearToken(); } catch (e) { ui.fail(e); }
  });
  refs.repoInput.addEventListener("keydown", (e) => { if (e.key === "Enter") { e.preventDefault(); refs.addBtn.click(); } });
  refs.openPluginsBtn.addEventListener("click", () => void ports.openPluginsFolder().catch(ui.fail));
  return { open };
}
