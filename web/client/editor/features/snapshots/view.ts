// Блок «Local snapshots» во вкладке Backup (ванильный DOM).
import { el } from "../../lib/dom.ts";
import type { Ui } from "../../lib/ui.ts";
import { formatSize } from "../cloud/model.ts";
import { MAX_LIMIT, parseLimit, snapshotLabel, type Snapshot, type SnapshotsInfo, type SnapshotsModel } from "./model.ts";

export interface SnapshotsRefs { box: HTMLElement }

export interface SnapshotsPorts {
  ui: Ui;
  /** Проверить снимок и открыть переходную модалку импорта. */
  openImport(text: string): Promise<void>;
}

export function mountSnapshots(refs: SnapshotsRefs, model: SnapshotsModel, ports: SnapshotsPorts) {
  const { ui } = ports;
  let info: SnapshotsInfo = { auto: true, max: 30, files: [] };

  const act = (btn: HTMLButtonElement, fn: () => Promise<unknown>) => {
    btn.addEventListener("click", async () => {
      btn.disabled = true;
      try { await fn(); } catch (e) { ui.fail(e); } finally { btn.disabled = false; }
    });
    return btn;
  };

  async function load() {
    try { info = await model.info(); } catch (e) { ui.fail(e); }
    render();
  }

  async function save(p: { auto?: boolean; max?: number }) {
    try { info = await model.save(p); } catch (e) { ui.fail(e); }
    render();
  }

  function row(s: Snapshot): HTMLElement {
    const open = act(el("button", { type: "button", textContent: "Import…" }) as HTMLButtonElement, async () => {
      await ports.openImport(await model.read(s.name));
    });
    const del = act(el("button", { type: "button", class: "danger", textContent: "Delete" }) as HTMLButtonElement, async () => {
      if (!confirm(`Delete this snapshot?\n${snapshotLabel(s, formatSize)}`)) return;
      info = await model.remove(s.name);
      render();
    });
    return el("div", { class: "row", style: "align-items:center;justify-content:space-between;gap:6px;border-top:1px solid var(--line);padding:6px 0" },
      el("div", { style: "flex:1;min-width:0", textContent: snapshotLabel(s, formatSize) }),
      el("div", { class: "row", style: "gap:6px" }, open, del));
  }

  function render() {
    const auto = el("input", { type: "checkbox", checked: info.auto }) as HTMLInputElement;
    auto.addEventListener("change", () => void save({ auto: auto.checked }));
    const max = el("input", { type: "number", min: "1", max: String(MAX_LIMIT), value: String(info.max), style: "width:70px" }) as HTMLInputElement;
    max.addEventListener("change", () => {
      const n = parseLimit(max.value);
      if (n === null) { ui.toast(`Enter a number from 1 to ${MAX_LIMIT}`); max.value = String(info.max); return; }
      void save({ max: n });
    });
    const now = act(el("button", { type: "button", textContent: "Save snapshot now" }) as HTMLButtonElement, async () => {
      info = await model.snapshot();
      ui.toast("Snapshot saved");
      render();
    });
    refs.box.replaceChildren(
      el("strong", { style: "font-size:12px", textContent: "Local snapshots" }),
      el("div", { class: "hint", textContent: "Go back to any earlier state of your settings. Snapshots stay on this computer, in the data folder." }),
      el("label", { class: "row", style: "gap:6px;align-items:center" }, auto, "Save a snapshot when the app closes, if settings have changed"),
      el("label", { class: "row", style: "gap:6px;align-items:center" }, "Keep at most", max, "files (older ones are deleted when a new one is saved)"),
      el("div", { class: "row", style: "gap:6px" }, now),
      ...(info.files.length ? info.files.map(row) : [el("div", { class: "hint", textContent: "No snapshots yet." })]),
    );
  }

  render();
  return { load };
}
