import { el } from "../../lib/dom.ts";
import type { Ui } from "../../lib/ui.ts";
import { isConflict, type DeckModel, type DeckState } from "./model.ts";
import { findTile, groupWidth, sizesFor, MAX_LABEL, MAX_NAME, type DeckGroup, type DeckTile } from "./ops.ts";

export interface DeckRefs {
  openBtn: HTMLElement;
  dialog: HTMLDialogElement;
  body: HTMLElement;
  saveBtn: HTMLButtonElement;
  resetBtn: HTMLElement;
  addGroupBtn: HTMLElement;
  closeBtn: HTMLElement;
  status: HTMLElement;
}

export interface DeckPorts {
  ui: Ui;
}

const CELL = 48; // px, клетка редактора
const GAP = 4;
const STEP = CELL + GAP;

const sizeKey = (s: { w: number; h: number }) => `${s.w}×${s.h}`;

/** Окно «Deck»: группы плиток с перетаскиванием и панель свойств выбранной плитки. Состояние — в модели, здесь только DOM. */
export function mountDeck(refs: DeckRefs, model: DeckModel, ports: DeckPorts): void {
  const { ui } = ports;
  let drag: { id: string; offX: number; offY: number } | null = null;

  function tileEl(st: DeckState, g: DeckGroup, t: DeckTile): HTMLElement {
    const color = st.palette[t.color ?? ""] ?? "#555";
    const glyph = st.icons[t.icon ?? ""] ?? "";
    const node = el("div", {
      class: "deckTile" + (st.selected === t.id ? " sel" : ""),
      draggable: true,
      title: `${t.label || t.action.scene || ""} (${sizeKey(t)})`,
      style: `position:absolute;left:${t.x * STEP}px;top:${t.y * STEP}px;width:${t.w * STEP - GAP}px;height:${t.h * STEP - GAP}px;`
        + `background:${color};color:#fff;border-radius:3px;box-sizing:border-box;padding:4px;overflow:hidden;font-size:12px;cursor:grab;`
        + (st.selected === t.id ? "outline:2px solid #fff;outline-offset:1px;" : ""),
    }, el("div", { style: "font-size:18px;line-height:1" }, glyph), el("div", {}, t.label || t.action.scene || "(empty)"), t.confirm ? el("div", { style: "opacity:.7" }, "confirm") : "");
    node.addEventListener("click", (e) => { e.stopPropagation(); model.select(t.id); });
    node.addEventListener("dragstart", (e) => {
      const r = node.getBoundingClientRect();
      drag = { id: t.id, offX: Math.floor((e.clientX - r.left) / STEP), offY: Math.floor((e.clientY - r.top) / STEP) };
      e.dataTransfer?.setData("text/plain", t.id);
      if (e.dataTransfer) e.dataTransfer.effectAllowed = "move";
    });
    node.addEventListener("dragend", () => { drag = null; });
    void g;
    return node;
  }

  function groupEl(st: DeckState, g: DeckGroup, index: number, count: number): HTMLElement {
    const cols = Math.min(st.grid.maxGroupCols, groupWidth(g) + 3);
    const name = el("input", { type: "text", value: g.name ?? "", maxLength: MAX_NAME, placeholder: "Group name", style: "width:130px" });
    name.addEventListener("change", () => model.renameGroup(g.id, name.value));
    const sizeSel = el("select", {}, ...st.grid.sizes.map((s) => el("option", { value: sizeKey(s) }, sizeKey(s))));
    const add = el("button", { type: "button", title: "Add a tile of this size" }, "+ Tile");
    add.addEventListener("click", () => {
      const size = st.grid.sizes.find((s) => sizeKey(s) === sizeSel.value) ?? st.grid.sizes[0];
      if (!model.addTile(g.id, size)) ui.toast("No room for a tile of this size in the group");
    });
    const left = el("button", { type: "button", title: "Move group left", disabled: index === 0 }, "◀");
    left.addEventListener("click", () => model.moveGroup(g.id, -1));
    const right = el("button", { type: "button", title: "Move group right", disabled: index === count - 1 }, "▶");
    right.addEventListener("click", () => model.moveGroup(g.id, 1));
    const del = el("button", { type: "button", title: "Delete group with its tiles" }, "✕");
    del.addEventListener("click", () => {
      if (g.tiles.length === 0 || confirm(`Delete the group with ${g.tiles.length} tile(s)?`)) model.removeGroup(g.id);
    });

    const grid = el("div", {
      class: "deckGrid",
      style: `position:relative;width:${cols * STEP - GAP}px;height:${st.grid.rows * STEP - GAP}px;`
        + `background-image:linear-gradient(90deg,transparent ${CELL}px,rgba(0,0,0,.35) ${CELL}px);background-size:${STEP}px ${STEP}px;`
        + "background-color:rgba(255,255,255,.06);",
    });
    grid.addEventListener("click", () => model.select(null));
    grid.addEventListener("dragover", (e) => { if (drag) e.preventDefault(); });
    grid.addEventListener("drop", (e) => {
      if (!drag) return;
      e.preventDefault();
      const r = grid.getBoundingClientRect();
      const x = Math.floor((e.clientX - r.left) / STEP) - drag.offX;
      const y = Math.floor((e.clientY - r.top) / STEP) - drag.offY;
      if (!model.moveTile(drag.id, g.id, x, y)) ui.toast("The tile doesn't fit there");
      drag = null;
    });
    for (const t of g.tiles) grid.append(tileEl(st, g, t));

    return el("div", { class: "deckGroup", style: "flex:none;margin-right:16px" },
      el("div", { class: "row", style: "margin-bottom:4px;gap:4px" }, name, left, right, sizeSel, add, del), grid);
  }

  function propsEl(st: DeckState): HTMLElement {
    const box = el("div", { class: "deckProps", style: "margin-top:12px;padding-top:8px;border-top:1px solid rgba(255,255,255,.15)" });
    const found = st.selected && st.layout ? findTile(st.layout, st.selected) : null;
    if (!found || !st.layout) {
      box.append(el("div", { class: "hint" }, "Select a tile to edit it. Drag tiles to move them (also between groups)."));
      return box;
    }
    const t = found.tile;
    const label = el("input", { type: "text", value: t.label ?? "", maxLength: MAX_LABEL, style: "width:200px" });
    label.addEventListener("change", () => model.updateTile(t.id, { label: label.value }));

    const scenes = st.scenes.includes(t.action.scene ?? "") || !t.action.scene ? st.scenes : [t.action.scene, ...st.scenes];
    const scene = el("select", {}, ...scenes.map((s) => el("option", { value: s, selected: s === t.action.scene }, s)));
    scene.addEventListener("change", () => model.updateTile(t.id, { scene: scene.value }));

    const colors = el("div", { class: "row", style: "gap:4px;flex-wrap:wrap" });
    for (const [name, hex] of Object.entries(st.palette)) {
      const b = el("button", {
        type: "button", title: name,
        style: `width:22px;height:22px;padding:0;background:${hex};border:2px solid ${t.color === name ? "#fff" : "transparent"};border-radius:3px`,
      });
      b.addEventListener("click", () => model.updateTile(t.id, { color: name }));
      colors.append(b);
    }

    const icon = el("select", {}, el("option", { value: "" }, "— none —"),
      ...Object.entries(st.icons).map(([n, g]) => el("option", { value: n, selected: n === t.icon }, `${g} ${n}`)));
    icon.addEventListener("change", () => model.updateTile(t.id, { icon: icon.value }));

    const size = el("select", {}, ...sizesFor(st.layout, t.id, st.grid).map((s) => el("option", { value: sizeKey(s), selected: s.w === t.w && s.h === t.h }, sizeKey(s))));
    size.addEventListener("change", () => {
      const s = st.grid.sizes.find((x) => sizeKey(x) === size.value);
      if (s) model.updateTile(t.id, { w: s.w, h: s.h });
    });

    const confirmBox = el("input", { type: "checkbox", checked: !!t.confirm });
    confirmBox.addEventListener("change", () => model.updateTile(t.id, { confirm: confirmBox.checked }));

    const del = el("button", { type: "button" }, "Delete tile");
    del.addEventListener("click", () => model.removeTile(t.id));

    const row = (title: string, ...c: (Node | string)[]) => el("div", { class: "row", style: "margin:4px 0;gap:8px" }, el("span", { style: "width:70px" }, title), ...c);
    box.append(
      row("Label", label), row("Scene", scene), row("Color", colors), row("Icon", icon), row("Size", size),
      row("Confirm", confirmBox, el("span", { class: "hint" }, "needs a second tap on /deck — for risky scenes")), del,
    );
    return box;
  }

  function render(st: DeckState) {
    refs.saveBtn.disabled = !st.dirty;
    refs.status.textContent = !st.loaded ? (st.error || "Loading…") : st.dirty ? "Unsaved changes" : st.saved ? "Saved" : "Default layout (not saved yet)";
    refs.body.replaceChildren();
    if (!st.layout) return;
    const strip = el("div", { style: "display:flex;align-items:flex-start;overflow-x:auto;padding-bottom:8px" });
    st.layout.groups.forEach((g, i, all) => strip.append(groupEl(st, g, i, all.length)));
    if (st.layout.groups.length === 0) strip.append(el("div", { class: "hint" }, "No groups. Add one."));
    refs.body.append(strip, propsEl(st));
  }
  model.subscribe(render);

  async function open() {
    refs.dialog.showModal();
    try { await model.load(); } catch (e) { ui.fail(e); }
  }
  refs.openBtn.addEventListener("click", () => void open());

  refs.addGroupBtn.addEventListener("click", () => { if (!model.addGroup()) ui.toast("Too many groups"); });

  refs.saveBtn.addEventListener("click", async () => {
    try {
      await model.save();
      ui.toast("Deck layout saved");
    } catch (e) {
      if (isConflict(e) && confirm("The layout was changed elsewhere. Reload it and lose your edits?")) {
        try { await model.load(); } catch (e2) { ui.fail(e2); }
      } else if (!isConflict(e)) ui.fail(e);
    }
  });

  refs.resetBtn.addEventListener("click", async () => {
    if (!confirm("Reset the deck to the default layout (one tile per OBS scene)? The saved layout will be lost.")) return;
    try { await model.reset(); ui.toast("Deck layout reset"); } catch (e) { ui.fail(e); }
  });

  // закрытие с несохранёнными правками — только после подтверждения
  const guard = () => !model.get().dirty || confirm("Discard unsaved deck changes?");
  refs.closeBtn.addEventListener("click", () => { if (guard()) refs.dialog.close(); });
  refs.dialog.addEventListener("cancel", (e) => { if (!guard()) e.preventDefault(); });
}
