import { el } from "../../lib/dom.ts";
import type { Ui } from "../../lib/ui.ts";
import { type ObsImportModel, reportLines, type ImportReport } from "./model.ts";

export interface ObsImportRefs {
  scene: HTMLSelectElement;
  btn: HTMLButtonElement;
  hint: HTMLElement;
  bind: HTMLInputElement;
  result: HTMLElement;
}

export interface ObsImportPorts {
  ui: Ui;
  /** После импорта: перечитать sources/layouts, закрыть настройки и открыть новый layout. */
  afterImport(r: ImportReport): Promise<void>;
}

export function mountObsImport(refs: ObsImportRefs, model: ObsImportModel, ports: ObsImportPorts) {
  /** Подгрузить список сцен (вызывается при открытии настроек). */
  async function load() {
    try {
      const scenes = await model.scenes();
      refs.scene.replaceChildren(...scenes.map((s) => el("option", { value: s, textContent: s })));
      refs.scene.disabled = refs.btn.disabled = scenes.length === 0;
      refs.hint.textContent = scenes.length
        ? "Browser Sources of the scene (including ones inside groups) become layers with the same size and position. External sites are added with proxy."
        : "No scenes yet — enable obs-websocket above.";
    } catch (e) {
      refs.scene.disabled = refs.btn.disabled = true;
      refs.hint.textContent = String(e);
    }
  }

  refs.btn.addEventListener("click", async () => {
    const scene = refs.scene.value;
    if (!scene) return;
    refs.btn.disabled = true;
    try {
      const r = await model.import(scene, refs.bind.checked);
      const lines = reportLines(r);
      refs.result.textContent = lines.join("\n");
      await ports.afterImport(r);
      ports.ui.toast(lines[0]);
    } catch (e) { ports.ui.fail(e); } finally { refs.btn.disabled = false; }
  });

  return { load };
}
