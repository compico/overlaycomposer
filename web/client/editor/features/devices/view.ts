import { el } from "../../lib/dom.ts";
import type { Ui } from "../../lib/ui.ts";
import type { DevicesInfo, DevicesModel } from "./model.ts";

export interface DevicesRefs {
  box: HTMLElement;
  pair: HTMLElement;
  code: HTMLButtonElement;
  left: HTMLElement;
  url: HTMLElement;
  hint: HTMLElement;
  list: HTMLElement;
}

export interface DevicesPorts {
  ui: Ui;
  /** Выбран ли в настройках адрес, доступный из сети (поле «Listen on», ещё не сохранённое). */
  wantsRemote(): boolean;
}

const CODE_MASK = "••••••";

export function mountDevices(refs: DevicesRefs, model: DevicesModel, ports: DevicesPorts) {
  let view: DevicesInfo | null = null;
  let codeShown = false;
  let codeTimer: ReturnType<typeof setTimeout> | undefined;

  function paintCode() {
    refs.code.textContent = codeShown && view?.code ? view.code : CODE_MASK;
    refs.code.classList.toggle("masked", !codeShown);
    refs.code.setAttribute("aria-pressed", String(codeShown));
  }

  function setCodeShown(show: boolean) {
    codeShown = show;
    clearTimeout(codeTimer);
    if (show) codeTimer = setTimeout(() => setCodeShown(false), 20000); // сам прячется — не остаётся на экране при демонстрации
    paintCode();
  }
  refs.code.addEventListener("click", () => setCodeShown(!codeShown));

  function paintHint() {
    const v = view;
    if (!v) return;
    const wantsRemote = ports.wantsRemote();
    refs.hint.textContent = v.remoteEnabled
      ? (wantsRemote ? "Devices that enter the code can open the editor and overlay from your network."
        : "You switched “Listen on” back to this computer only: pairing will stop after Save and restart. Already paired devices stay in the list until revoked.")
      : (wantsRemote ? "Pairing will be available after you Save and restart the application."
        : "Pairing is off while the server listens on this computer only. To connect another device (tablet, phone), set “Listen on” to “All network interfaces”, Save and restart.");
  }

  model.subscribe((v) => {
    view = v;
    refs.box.hidden = !v;
    if (!v) return;
    refs.pair.hidden = !v.remoteEnabled;
    if (!v.remoteEnabled && codeShown) setCodeShown(false);
    paintCode();
    paintHint();
    refs.left.textContent = v.remoteEnabled ? `changes in ${v.expiresIn}s` : "";
    refs.url.textContent = v.pairUrl.replace("127.0.0.1", location.hostname === "127.0.0.1" || location.hostname === "localhost" ? "<this-computer-ip>" : location.hostname);
    refs.list.replaceChildren(...v.devices.map((d) => {
      const seen = new Date(d.lastSeen).toLocaleString();
      const rm = el("button", { type: "button", textContent: "Revoke" });
      rm.addEventListener("click", async () => {
        if (!confirm(`Revoke access for “${d.name}”?`)) return;
        try { await model.revoke(d.id); } catch (e) { ports.ui.fail(e); }
      });
      return el("div", { class: "row", style: "justify-content:space-between" },
        el("span", { class: "hint", textContent: `${d.name} · ${d.lastIp} · seen ${seen}` }), rm);
    }));
  });

  return { hideCode: () => setCodeShown(false), repaintHint: paintHint };
}
