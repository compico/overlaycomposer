import type { PluginManifest, Source } from "../../../../shared/types.ts";
import { pluginKey } from "../../../shared/pluginkey.ts";
import { el } from "../../lib/dom.ts";
import { type AddSourceModel, defaultWidgetName, existingMeta, type PickedFile, pickSelection, pluginHint, pluginTitle, stripCommonRoot } from "./model.ts";

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
  htmlFiles: HTMLInputElement;
  htmlFolder: HTMLInputElement;
  htmlPickFilesBtn: HTMLElement;
  htmlPickFolderBtn: HTMLElement;
  htmlPicked: HTMLElement;
  htmlName: HTMLInputElement;
  htmlCreateBtn: HTMLElement;
}

export type AddTab = "iframe" | "existing" | "plugin" | "html";

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
    selectedPluginId = pickSelection(plugins.map(pluginKey), selectedPluginId);
    refs.pluginList.replaceChildren(...plugins.map((p) => pickItem(
      pluginKey(p) === selectedPluginId, pluginTitle(p), p.description ?? "",
      () => { selectedPluginId = pluginKey(p); renderPlugins(); },
      () => refs.usePluginBtn.click(),
    )));
    if (plugins.length === 0) {
      refs.pluginList.replaceChildren(el("div", { class: "empty", textContent: "No widgets installed yet. Open the Widget store to install some, or put a widget folder (with plugin.json) into the widgets folder." }));
    }
    const p = plugins.find((x) => pluginKey(x) === selectedPluginId);
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

  // ---------- HTML-файл ----------
  let picked: PickedFile[] = [];
  /** Только имя папки/файла сверху: для имени по умолчанию. */
  let pickedFolder = "";

  function showPicked() {
    const bytes = picked.reduce((n, f) => n + f.file.size, 0);
    refs.htmlPicked.textContent = picked.length === 0
      ? "Nothing selected."
      : `${picked.length} file${picked.length === 1 ? "" : "s"}, ${bytes < 1024 ? `${bytes} B` : `${(bytes / 1024).toFixed(bytes < 10240 ? 1 : 0)} KB`}`;
  }
  function takeFiles(input: HTMLInputElement, folder: boolean) {
    const list = [...(input.files ?? [])];
    input.value = ""; // тот же выбор можно повторить
    if (list.length === 0) return;
    const raw = list.map((f) => ({ path: folder ? (f.webkitRelativePath || f.name) : f.name, file: f as Blob }));
    pickedFolder = folder ? (raw[0].path.split("/")[0] ?? "") : "";
    picked = folder ? stripCommonRoot(raw) : raw;
    if (!refs.htmlName.value.trim()) refs.htmlName.value = defaultWidgetName(picked, pickedFolder);
    showPicked();
  }
  refs.htmlPickFilesBtn.addEventListener("click", () => refs.htmlFiles.click());
  refs.htmlPickFolderBtn.addEventListener("click", () => refs.htmlFolder.click());
  refs.htmlFiles.addEventListener("change", () => takeFiles(refs.htmlFiles, false));
  refs.htmlFolder.addEventListener("change", () => takeFiles(refs.htmlFolder, true));
  refs.htmlCreateBtn.addEventListener("click", async () => {
    try {
      const ok = await model.createLocalHtml({ name: refs.htmlName.value, files: picked });
      if (!ok) return ports.toast("Choose a file and enter a name");
      refs.dialog.close();
      picked = [];
      refs.htmlName.value = "";
      showPicked();
    } catch (e) { ports.fail(e); }
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
