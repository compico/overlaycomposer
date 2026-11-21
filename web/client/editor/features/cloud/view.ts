// Блок «Cloud storage» (Google Drive или папка) во вкладке Backup (ванильный DOM). Подтверждения и тосты — здесь: это UI, а не модель.
import { el } from "../../lib/dom.ts";
import { openExternal } from "../store/deprecated-view.ts";
import { isConflict } from "../../lib/api.ts";
import type { Ui } from "../../lib/ui.ts";
import { fileLabel, providerLabel, type CloudFile, type CloudModel, type CloudStatus } from "./model.ts";

export interface CloudRefs { box: HTMLElement }

export interface CloudPorts {
  ui: Ui;
  /** Секции, отмеченные в блоке экспорта: их и грузим. */
  sections(): string[];
  /** Проверить скачанный файл и открыть переходную модалку импорта. */
  openImport(text: string): Promise<void>;
}

export function mountCloud(refs: CloudRefs, model: CloudModel, ports: CloudPorts) {
  const { ui } = ports;
  let status: CloudStatus = { provider: "", connected: false };
  let files: CloudFile[] = [];
  let waiting: ReturnType<typeof setInterval> | undefined; // опрос статуса, пока пользователь соглашается на странице Google
  const stopWaiting = () => { if (waiting !== undefined) clearInterval(waiting); waiting = undefined; };

  const act = (btn: HTMLButtonElement, fn: () => Promise<unknown>) => {
    btn.addEventListener("click", async () => {
      btn.disabled = true;
      try { await fn(); } catch (e) { ui.fail(e); await load(); /* подключение могло сброситься (токен устарел) */ } finally { btn.disabled = false; }
    });
    return btn;
  };

  async function connectGoogle() {
    openExternal(await model.googleStart()); // https-адрес Google; noopener внутри
    ui.toast("Finish connecting in the browser tab that opened…");
    stopWaiting();
    const until = Date.now() + 3 * 60_000;
    waiting = setInterval(async () => {
      try {
        const s = await model.status();
        if (s.connected && s.provider === "google") { stopWaiting(); ui.toast("Google Drive connected"); await load(); }
        else if (Date.now() > until) stopWaiting();
      } catch { stopWaiting(); }
    }, 2000);
  }

  async function load() {
    stopWaiting();
    try {
      status = await model.status();
      files = status.connected ? await model.list() : [];
    } catch (e) { ui.fail(e); }
    render();
  }

  async function upload(over?: CloudFile) {
    const sections = ports.sections();
    if (!sections.length) { ui.toast("Tick at least one part in “Export settings”"); return; }
    try {
      await model.upload({ sections, ...(over ? { name: over.name, expect: over.fingerprint } : {}) });
      ui.toast(over ? "File replaced" : "Settings uploaded");
    } catch (e) {
      if (isConflict(e)) ui.toast("The file was changed in the folder meanwhile — nothing was overwritten. The list is refreshed.");
      else ui.fail(e);
    }
    await load();
  }

  function fileRow(f: CloudFile): HTMLElement {
    const get = act(el("button", { type: "button", textContent: "Import…" }) as HTMLButtonElement, async () => {
      await ports.openImport(await model.download(f.id));
    });
    const replace = act(el("button", { type: "button", textContent: "Replace" }) as HTMLButtonElement, async () => {
      if (!confirm(`Replace “${f.name}” with your current settings?\nThe old content of this file will be lost.`)) return;
      await upload(f);
    });
    const del = act(el("button", { type: "button", class: "danger", textContent: "Delete" }) as HTMLButtonElement, async () => {
      if (!confirm(`Delete “${f.name}” from the folder?`)) return;
      await model.remove(f.id);
      await load();
    });
    return el("div", { class: "row", style: "align-items:center;justify-content:space-between;gap:6px;border-top:1px solid var(--line);padding:6px 0" },
      el("div", { style: "flex:1;min-width:0;word-break:break-all", textContent: fileLabel(f) }),
      el("div", { class: "row", style: "gap:6px" }, get, replace, del));
  }

  function render() {
    const parts: HTMLElement[] = [
      el("strong", { style: "font-size:12px", textContent: "Cloud storage" }),
      el("div", { class: "hint", textContent: "Keep settings files in Google Drive or in a folder that Dropbox, OneDrive or Syncthing synchronizes — your settings then travel between computers. Nothing is uploaded or changed automatically." }),
    ];
    if (!status.connected) {
      const g = status.google;
      const gBtn = act(el("button", { type: "button", class: "primary", textContent: "Connect Google Drive" }) as HTMLButtonElement, connectGoogle);
      gBtn.disabled = !g?.available;
      parts.push(
        el("div", { class: "row", style: "gap:6px;align-items:center" }, gBtn,
          el("span", { class: "hint", textContent: g?.available ? "Files go to a hidden app folder in your Drive: they are not shown among your files and only this app can read them." : "Not available in this build (no Google OAuth client)." })),
        el("div", { class: "hint", textContent: "or use a synchronized folder:" }),
      );
      const input = el("input", { type: "text", placeholder: "Full path, e.g. C:\\Users\\you\\Dropbox\\overlay-composer", style: "flex:1;min-width:0", value: status.folder ?? "" }) as HTMLInputElement;
      const connect = act(el("button", { type: "button", textContent: "Connect" }) as HTMLButtonElement, async () => {
        if (!input.value.trim()) return;
        status = await model.connect(input.value);
        await load();
      });
      parts.push(el("div", { class: "row", style: "gap:6px" }, input, connect));
      if (status.folder) parts.push(el("div", { class: "hint warn", textContent: `The folder ${status.folder} is not available right now.` }));
    } else {
      const upBtn = act(el("button", { type: "button", class: "primary", textContent: "Upload settings" }) as HTMLButtonElement, () => upload());
      const refresh = act(el("button", { type: "button", textContent: "Refresh" }) as HTMLButtonElement, load);
      const off = act(el("button", { type: "button", textContent: "Disconnect" }) as HTMLButtonElement, async () => {
        status = await model.disconnect();
        await load();
      });
      parts.push(
        el("div", { class: "hint", style: "word-break:break-all", textContent: providerLabel(status) }),
        el("div", { class: "row", style: "gap:6px" }, upBtn, refresh, off),
        el("div", { class: "hint", textContent: "Upload adds a new file with the parts ticked in “Export settings”; Replace overwrites a file you choose." }),
      );
      if (!files.length) parts.push(el("div", { class: "hint", textContent: "No settings files yet." }));
      for (const f of files) parts.push(fileRow(f));
    }
    refs.box.replaceChildren(...parts);
  }

  render();
  return { load };
}
