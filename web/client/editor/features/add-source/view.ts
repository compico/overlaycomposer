import type { PluginManifest, Source } from "../../../../shared/types.ts";
import { el } from "../../lib/dom.ts";
import { type AddSourceModel, existingMeta, pickSelection, pluginHint, pluginTitle } from "./model.ts";

export interface AddSourceRefs {
  dialog: HTMLDialogElement;
  tabs: HTMLElement[];
  panes: Record<AddTab, HTMLElement>;
  tabExisting: HTMLElement;
  newName: HTMLInputElement;
  newUrl: HTMLInputElement;
  newProxy: HTMLInputElement;
  newW: HTMLInputElement;
  newH: HTMLInputElement;
  createBtn: HTMLElement;
  existingList: HTMLElement;
  useExistingBtn: HTMLElement;
  pluginList: HTMLElement;
  pluginHint: HTMLElement;
  usePluginBtn: HTMLElement;
  openBtn: HTMLElement;
}

export type AddTab = "iframe" | "existing" | "plugin";

export interface AddSourcePorts {
  sources(): Source[];
  plugins(): PluginManifest[];
  /** Перечитать список плагинов перед показом диалога. */
  refreshPlugins(): Promise<void>;
  toast(msg: string): void;
  fail(e: unknown): void;
}

function pickItem(sel: boolean, title: string, meta: string, onPick: () => void, onDouble?: () => void): HTMLElement {
  const it = el("div", { class: `item${sel ? " sel" : ""}`, tabIndex: 0 },
    el("div", { style: "flex:1;min-width:0" }, el("div", { class: "name", textContent: title }), el("div", { class: "meta", textContent: meta })));
  it.addEventListener("click", onPick);
  it.addEventListener("keydown", (e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); onPick(); } });
  if (onDouble) it.addEventListener("dblclick", onDouble);
  return it;
}

export function mountAddSource(refs: AddSourceRefs, model: AddSourceModel, ports: AddSourcePorts) {
  let selectedPluginId = "";
  let selectedExistingId = "";

  function setTab(tab: AddTab) {
    if (tab === "existing" && ports.sources().length === 0) tab = "iframe";
    for (const b of refs.tabs) {
      const on = b.dataset.tab === tab;
      b.classList.toggle("sel", on);
      b.setAttribute("aria-selected", String(on));
    }
    for (const t of Object.keys(refs.panes) as AddTab[]) refs.panes[t].hidden = t !== tab;
    if (tab === "iframe") refs.newName.focus();
  }
  for (const b of refs.tabs) b.addEventListener("click", () => setTab(b.dataset.tab as AddTab));

  function renderExisting() {
    const sources = ports.sources();
    selectedExistingId = pickSelection(sources.map((x) => x.id), selectedExistingId);
    refs.existingList.replaceChildren(...sources.map((x) => pickItem(
      x.id === selectedExistingId, x.name, existingMeta(x),
      () => { selectedExistingId = x.id; renderExisting(); },
      () => refs.useExistingBtn.click(),
    )));
    if (sources.length === 0) refs.existingList.replaceChildren(el("div", { class: "empty", textContent: "No sources yet — create one on the first tab." }));
    refs.useExistingBtn.toggleAttribute("disabled", sources.length === 0);
    refs.tabExisting.toggleAttribute("disabled", sources.length === 0);
  }

  function renderPlugins() {
    const plugins = ports.plugins();
    selectedPluginId = pickSelection(plugins.map((x) => x.id), selectedPluginId);
    refs.pluginList.replaceChildren(...plugins.map((p) => pickItem(
      p.id === selectedPluginId, pluginTitle(p), p.description ?? "",
      () => { selectedPluginId = p.id; renderPlugins(); },
      () => refs.usePluginBtn.click(),
    )));
    if (plugins.length === 0) {
      refs.pluginList.replaceChildren(el("div", { class: "empty", textContent: "No plugins installed yet. Open the Plugin store to install some, or put a plugin folder (with plugin.json) into the plugins folder." }));
    }
    const p = plugins.find((x) => x.id === selectedPluginId);
    refs.pluginHint.textContent = pluginHint(p);
    refs.usePluginBtn.toggleAttribute("disabled", !p);
  }

  refs.openBtn.addEventListener("click", async () => {
    await ports.refreshPlugins();
    renderPlugins();
    renderExisting();
    refs.dialog.showModal();
    setTab("iframe"); // по умолчанию — обычная страница по адресу
  });

  for (const input of [refs.newName, refs.newUrl]) {
    input.addEventListener("keydown", (e) => { if (e.key === "Enter") { e.preventDefault(); refs.createBtn.click(); } });
  }

  refs.useExistingBtn.addEventListener("click", () => {
    model.useExisting(selectedExistingId);
    refs.dialog.close();
  });

  refs.usePluginBtn.addEventListener("click", async () => {
    try { if (await model.usePlugin(selectedPluginId)) refs.dialog.close(); } catch (e) { ports.fail(e); }
  });

  refs.createBtn.addEventListener("click", async () => {
    try {
      const ok = await model.createIframe({
        name: refs.newName.value, url: refs.newUrl.value, proxy: refs.newProxy.checked,
        w: Number(refs.newW.value), h: Number(refs.newH.value),
      });
      if (!ok) return ports.toast("Name and URL are required");
      refs.dialog.close();
      refs.newName.value = "";
      refs.newUrl.value = "";
      refs.newProxy.checked = false;
    } catch (e) { ports.fail(e); }
  });

  return { renderPlugins, renderExisting };
}
