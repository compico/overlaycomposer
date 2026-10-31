import { el } from "../../lib/dom.ts";
import { addScene, type LayoutRef, sceneRows, setBound } from "./model.ts";

export interface SceneBindingPorts {
  /** Редактируемый layout (рабочая копия): представление меняет его `scenes`. */
  current(): (LayoutRef & { scenes: string[] }) | null;
  layouts(): LayoutRef[];
  obsScenes(): string[];
  /** Привязки изменены (пометить layout как несохранённый). */
  changed(): void;
}

/** Блок «OBS scenes» в свойствах layout. Перерисовывается отдельно: список сцен и привязки приходят асинхронно. */
export function mountSceneBinding(box: () => HTMLElement | null, ports: SceneBindingPorts) {
  function render() {
    const root = box();
    const cur = ports.current();
    if (!root || !cur) return;
    const obsScenes = ports.obsScenes();
    const rows = sceneRows(cur, ports.layouts(), obsScenes).map((r) => {
      const cb = el("input", { type: "checkbox", checked: r.checked });
      cb.addEventListener("change", () => {
        cur.scenes = setBound(cur.scenes, r.name, cb.checked);
        ports.changed();
        render();
      });
      return el("label", { class: "chk" }, cb, ` ${r.name}`, r.notes.length ? el("span", { class: "hint", textContent: ` — ${r.notes.join(", ")}` }) : "");
    });

    const add = el("input", { type: "text", placeholder: "Add scene by name…" });
    const addBtn = el("button", { type: "button", textContent: "Add" });
    const doAdd = () => {
      const next = addScene(cur.scenes, add.value);
      if (next === cur.scenes) return;
      cur.scenes = next;
      ports.changed();
      render();
    };
    addBtn.addEventListener("click", doAdd);
    add.addEventListener("keydown", (e) => { if (e.key === "Enter") { e.preventDefault(); doAdd(); } });

    root.replaceChildren(
      el("div", { class: "hint", textContent: obsScenes.length
        ? "Show this layout when OBS switches to the checked scenes:"
        : "OBS scene list is not available yet (connect OBS in Settings). You can still add scene names by hand:" }),
      ...(rows.length ? rows : [el("div", { class: "empty", textContent: "No scenes bound — this layout is shown only by ▶ or as the default." })]),
      el("div", { class: "row", style: "margin-top:4px" }, add, addBtn),
    );
  }
  return { render };
}
