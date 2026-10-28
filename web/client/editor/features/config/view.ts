import { el } from "../../lib/dom.ts";
import type { Ui } from "../../lib/ui.ts";
import type { ConfigModel, ConfigView } from "./model.ts";

export interface ConfigRefs {
  openBtn: HTMLElement;
  obsBadge: HTMLElement;
  dialog: HTMLDialogElement;
  status: HTMLElement;
  wsStatus: HTMLElement;
  overlayUrl: HTMLElement;
  debugProxy: HTMLInputElement;
  obsEnabled: HTMLInputElement;
  obsWsBox: HTMLElement;
  obsUrl: HTMLInputElement;
  obsPass: HTMLInputElement;
  obsClear: HTMLInputElement;
  obsClearRow: HTMLElement;
  host: HTMLSelectElement;
  hostWarn: HTMLElement;
  port: HTMLInputElement;
  saveBtn: HTMLElement;
  reconnectBtn: HTMLElement;
}

export interface ConfigPorts {
  ui: Ui;
  /** Диалог открывается: подгрузить остальные разделы настроек (приложение, импорт, устройства, Twitch). */
  onOpen(): void;
  /** Раз в 2 с, пока диалог открыт: обновить зависящие от сервера разделы. */
  onTick(): void;
  onClose(): void;
  /** «Listen on» изменён (или заполнен): пересчитать подсказки, зависящие от адреса. */
  onHostChange(): void;
  /** Свежие сведения об OBS (бейдж в шапке, список сцен). */
  onObsInfo(obs: ConfigView["obs"]): void;
}

export function mountConfig(refs: ConfigRefs, model: ConfigModel, ports: ConfigPorts) {
  const { ui } = ports;
  let timer = 0;

  function showStatus(c: ConfigView) {
    refs.status.className = `hint ${c.obs.connected ? "" : "warn"}`;
    refs.status.textContent = c.obs.connected
      ? `OBS: connected via ${c.obs.via === "overlay" ? "overlay" : "obs-websocket"}` +
        (c.obs.scene ? ` · scene: ${c.obs.scene}` : "") +
        (c.obs.scenes.length ? ` · ${c.obs.scenes.length} scenes known` : "")
      : `OBS: no data — ${c.obs.error ?? "waiting…"}`;
    if (c.restartRequired) refs.status.textContent += " · restart pending for host/port";
    refs.wsStatus.className = `hint ${c.obsWsConnected ? "" : "warn"}`;
    refs.wsStatus.textContent = c.obsWsConnected
      ? "obs-websocket: connected"
      : `obs-websocket: not connected${c.obsWsError ? ` — ${c.obsWsError}` : " (connecting…)"}`;
    refs.overlayUrl.textContent = c.overlayUrl;
    ports.onObsInfo(c.obs);
  }

  // Статус обновляется при любом новом значении модели (в т.ч. при тихом опросе).
  model.subscribe((c) => { if (c) showStatus(c); });

  function updateHostWarn() {
    refs.hostWarn.hidden = refs.host.value === "127.0.0.1";
    ports.onHostChange();
  }
  refs.host.addEventListener("change", updateHostWarn);
  refs.obsEnabled.addEventListener("change", () => { refs.obsWsBox.hidden = !refs.obsEnabled.checked; });

  refs.dialog.addEventListener("close", () => { clearInterval(timer); ports.onClose(); });

  refs.reconnectBtn.addEventListener("click", async () => {
    try {
      await model.reconnectObs();
      refs.wsStatus.textContent = "obs-websocket: connecting…";
      setTimeout(() => void model.refresh(), 1500);
    } catch (e) { ui.fail(e); }
  });

  // Клик по значку OBS в шапке открывает настройки с подробностями.
  refs.obsBadge.addEventListener("click", () => refs.openBtn.click());

  refs.openBtn.addEventListener("click", async () => {
    try {
      const c = await model.load();
      refs.debugProxy.checked = c.debugProxy;
      refs.obsEnabled.checked = c.obsEnabled;
      refs.obsWsBox.hidden = !c.obsEnabled;
      refs.obsUrl.value = c.obsUrl;
      refs.obsPass.value = "";
      refs.obsPass.placeholder = c.hasObsPassword ? "saved — leave empty to keep" : "no password";
      refs.obsClear.checked = false;
      refs.obsClearRow.hidden = !c.hasObsPassword;
      const hosts = [
        ["127.0.0.1", "This computer only (recommended)"],
        ["0.0.0.0", "All network interfaces"],
      ];
      if (!hosts.some(([v]) => v === c.host)) hosts.push([c.host, c.host]);
      refs.host.replaceChildren(...hosts.map(([v, t]) => el("option", { value: v, textContent: t, selected: v === c.host })));
      updateHostWarn();
      refs.port.value = String(c.port);
      ports.onOpen();
      refs.dialog.showModal();
      clearInterval(timer);
      timer = setInterval(() => { void model.refresh(); ports.onTick(); }, 2000) as unknown as number;
    } catch (e) { ui.fail(e); }
  });

  refs.saveBtn.addEventListener("click", async () => {
    try {
      const c = await model.save({
        host: refs.host.value,
        port: Number(refs.port.value),
        debugProxy: refs.debugProxy.checked,
        obs: { enabled: refs.obsEnabled.checked, url: refs.obsUrl.value.trim(), password: refs.obsPass.value, clearPassword: refs.obsClear.checked },
      });
      refs.dialog.close();
      ui.toast(c.restartRequired ? "Saved. Restart the server to apply host/port" : "Settings saved");
    } catch (e) { ui.fail(e); }
  });

  return { closeDialog: () => refs.dialog.close() };
}
