import { el } from "../../lib/dom.ts";
import type { Ui } from "../../lib/ui.ts";
import type { AppModel } from "./model.ts";

export interface AppRefs {
  version: HTMLElement;
  paths: HTMLElement;
  autostart: HTMLInputElement;
  autostartHint: HTMLElement;
  trayHint: HTMLElement;
  openData: HTMLElement;
  openLogs: HTMLElement;
  quit: HTMLElement;
}

export function mountApp(refs: AppRefs, model: AppModel, ui: Ui) {
  let updateToasted = false;
  model.subscribe((a) => {
    if (!a) return;
    refs.version.textContent = a.version ? `Version: ${a.version}` : "";
    if (a.update?.available && /^https:\/\/github\.com\//.test(a.update.url)) {
      const link = el("a", { href: a.update.url, target: "_blank", rel: "noopener noreferrer", textContent: `New version ${a.update.latest} is available` });
      refs.version.append(" — ", link);
      if (!updateToasted) { updateToasted = true; ui.toast(`New version ${a.update.latest} is available (Settings)`); }
    }
    refs.paths.textContent = `Config: ${a.configFile}\nData: ${a.dataDir}\nLog: ${a.logFile}`;
    refs.autostart.checked = a.autostart === true;
    refs.autostart.disabled = a.autostart === null;
    refs.autostartHint.textContent = a.autostart === null
      ? "Autostart is available in the packaged app (Windows, Linux)."
      : "Starts minimized (no browser window) when you log in.";
    refs.trayHint.textContent = a.tray
      ? "The app lives in the system tray (right-click the icon)."
      : a.os === "windows" && a.compiled ? "Tray icon failed to start — see the log." : "";
  });
  refs.autostart.addEventListener("change", async () => {
    const want = refs.autostart.checked;
    try { await model.setAutostart(want); } catch (err) { refs.autostart.checked = !want; ui.fail(err); }
  });
  refs.openData.addEventListener("click", () => void model.open("data").catch(ui.fail));
  refs.openLogs.addEventListener("click", () => void model.open("logs").catch(ui.fail));
  refs.quit.addEventListener("click", async () => {
    if (!confirm("Quit Overlay Composer? Overlays in OBS will stop updating until it is started again.")) return;
    await model.quit();
    document.body.textContent = "Overlay Composer has been stopped. You can close this tab.";
  });
}
