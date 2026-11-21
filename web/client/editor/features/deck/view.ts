import { el } from "../../lib/dom.ts";
import type { Ui } from "../../lib/ui.ts";
import { isDeleteTile } from "./keys.ts";
import { isConflict, type DeckModel, type DeckState } from "./model.ts";
import { findTile, groupWidth, sizesFor, tileTitle, unusedScenes, MAX_FOLDER_SCENES, MAX_LABEL, MAX_NAME, type DeckGroup, type DeckTile } from "./ops.ts";

export interface DeckRefs {
  openBtn: HTMLElement;
  /** Обычное рабочее поле редактора (скрывается в режиме Deck) и поле редактора deck. */
  editorMain: HTMLElement;
  deckMain: HTMLElement;
  /** Элементы шапки, которые относятся к раскладкам оверлея и в режиме Deck прячутся. */
  hideInDeck: HTMLElement[];
  groupList: HTMLElement;
  addGroupBtn: HTMLElement;
  sizeBtns: HTMLElement;
  tileList: HTMLElement;
  canvas: HTMLElement;
  props: HTMLElement;
  saveBtn: HTMLButtonElement;
  resetBtn: HTMLElement;
  status: HTMLElement;
}

export interface DeckPorts {
  ui: Ui;
}

const CELL = 60; // px, клетка редактора
const GAP = 4;
const STEP = CELL + GAP;

const sizeKey = (s: { w: number; h: number }) => `${s.w}×${s.h}`;

/**
 * Режим «Deck» редактора: слева группы и добавление плиток, в центре холст (группы слева направо, как на планшете),
 * справа свойства выбранной плитки или группы. Состояние — в модели, здесь только DOM.
 */
export function mountDeck(refs: DeckRefs, model: DeckModel, ports: DeckPorts): { isActive(): boolean } {
  const { ui } = ports;
  let active = false;
  let curGroup: string | null = null; // группа, в которую добавляются плитки и свойства которой видны
  let drag: { id: string; offX: number; offY: number } | null = null;

  /** Текущая группа: выбранная, иначе группа выбранной плитки, иначе первая. */
  function currentGroup(st: DeckState): DeckGroup | null {
    const l = st.layout;
    if (!l) return null;
    const sel = st.selected ? findTile(l, st.selected) : null;
    if (sel) return sel.group;
    return l.groups.find((g) => g.id === curGroup) ?? l.groups[0] ?? null;
  }

  function tileEl(st: DeckState, t: DeckTile): HTMLElement {
    const color = st.palette[t.color ?? ""] ?? "#555";
    const glyph = st.icons[t.icon ?? ""] ?? "";
    const node = el("div", {
      class: "deckTile" + (st.selected === t.id ? " sel" : ""),
      draggable: true,
      title: `${tileTitle(t)} (${sizeKey(t)})`,
      style: `position:absolute;left:${t.x * STEP}px;top:${t.y * STEP}px;width:${t.w * STEP - GAP}px;height:${t.h * STEP - GAP}px;`
        + `background:${color};color:#fff;border-radius:3px;box-sizing:border-box;padding:5px;overflow:hidden;font-size:12px;cursor:grab;`,
    }, el("div", { style: "font-size:20px;line-height:1" }, glyph), el("div", {}, tileTitle(t) || "(empty)"),
    t.confirm ? el("div", { style: "opacity:.7;font-size:10px" }, "confirm") : "");
    node.addEventListener("click", (e) => { e.stopPropagation(); model.select(t.id); });
    node.addEventListener("dragstart", (e) => {
      const r = node.getBoundingClientRect();
      drag = { id: t.id, offX: Math.floor((e.clientX - r.left) / STEP), offY: Math.floor((e.clientY - r.top) / STEP) };
      e.dataTransfer?.setData("text/plain", t.id);
      if (e.dataTransfer) e.dataTransfer.effectAllowed = "move";
    });
    node.addEventListener("dragend", () => { drag = null; });
    return node;
  }

  function groupEl(st: DeckState, g: DeckGroup, current: boolean): HTMLElement {
    // ширина холста группы: занятые столбцы + один запасной (чтобы было куда перенести плитку), но не меньше 4 — группа не «ползёт» при каждом движении
    const cols = Math.min(st.grid.maxGroupCols, Math.max(groupWidth(g) + 1, 4));
    const grid = el("div", {
      style: `position:relative;width:${cols * STEP - GAP}px;height:${st.grid.rows * STEP - GAP}px;`
        + `background-image:linear-gradient(90deg,transparent ${CELL}px,rgba(0,0,0,.45) ${CELL}px),linear-gradient(180deg,transparent ${CELL}px,rgba(0,0,0,.45) ${CELL}px);`
        + `background-size:${STEP}px ${STEP}px;background-color:rgba(255,255,255,.07);`,
    });
    grid.addEventListener("click", () => { curGroup = g.id; model.select(null); });
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
    for (const t of g.tiles) grid.append(tileEl(st, t));
    const title = el("div", { class: "gname", style: `margin-bottom:6px;cursor:pointer;color:${current ? "var(--text)" : "var(--muted)"};font-size:15px` }, g.name || "(no name)");
    title.addEventListener("click", () => { curGroup = g.id; model.select(null); });
    return el("div", { class: "deckGroup" + (current ? " cur" : ""), style: "flex:none;margin-right:28px" }, title, grid);
  }

  function groupProps(st: DeckState, g: DeckGroup, index: number, count: number): HTMLElement {
    const box = el("div", {});
    const name = el("input", { type: "text", value: g.name ?? "", maxLength: MAX_NAME, style: "width:100%" });
    name.addEventListener("change", () => model.renameGroup(g.id, name.value));
    const left = el("button", { type: "button", disabled: index === 0 }, "◀ Earlier");
    left.addEventListener("click", () => model.moveGroup(g.id, -1));
    const right = el("button", { type: "button", disabled: index === count - 1 }, "Later ▶");
    right.addEventListener("click", () => model.moveGroup(g.id, 1));
    const del = el("button", { type: "button", class: "danger" }, "Delete group");
    del.addEventListener("click", () => {
      if (g.tiles.length === 0 || confirm(`Delete the group with ${g.tiles.length} tile(s)?`)) model.removeGroup(g.id);
    });
    box.append(el("h3", {}, "Group"), el("label", {}, "Title"), name, el("div", { class: "row" }, left, right), el("div", { class: "row" }, del),
      el("div", { class: "hint" }, "Select a tile to edit it. Drag tiles to move them, also between groups."));
    return box;
  }

  /** Выбор сцен папки: флажки по списку OBS (порядок — как отметили), без OBS — по одному имени в строке. Сцен от 1 до MAX_FOLDER_SCENES. */
  function folderEditor(t: DeckTile, chosen: string[], obsScenes: string[]): HTMLElement {
    const hint = el("div", { class: "hint" }, `Tapping the tile on the panel opens these scenes (1 to ${MAX_FOLDER_SCENES}), in the order you picked them.`);
    if (obsScenes.length === 0) {
      const area = el("textarea", { rows: 5, style: "width:100%", placeholder: "One scene name per line" }) as HTMLTextAreaElement;
      area.value = chosen.join("\n");
      area.addEventListener("change", () => model.updateTile(t.id, { folder: area.value.split("\n").map((s) => s.trim()) }));
      return el("div", {}, el("label", {}, "Scenes"), area, hint,
        el("div", { class: "hint" }, "The scene list is unavailable (OBS is not connected?). Type the names exactly as in OBS."));
    }
    const names = [...chosen.filter((s) => !obsScenes.includes(s)), ...obsScenes]; // сцены, которых уже нет в OBS, остаются видны и их можно снять
    const full = chosen.length >= MAX_FOLDER_SCENES;
    const list = el("div", { style: "max-height:220px;overflow:auto" });
    for (const s of names) {
      const on = chosen.includes(s);
      const box = el("input", { type: "checkbox", checked: on, disabled: (!on && full) || (on && chosen.length === 1) });
      box.addEventListener("change", () => model.updateTile(t.id, { folder: box.checked ? [...chosen, s] : chosen.filter((x) => x !== s) }));
      list.append(el("label", { class: "chk", style: "display:flex;gap:6px;align-items:center;margin:2px 0" }, box, s));
    }
    return el("div", {}, el("label", {}, `Scenes (${chosen.length}/${MAX_FOLDER_SCENES})`), list, hint);
  }

  function tileProps(st: DeckState, t: DeckTile): HTMLElement {
    const layout = st.layout!;
    const label = el("input", { type: "text", value: t.label ?? "", maxLength: MAX_LABEL, style: "width:100%" });
    label.addEventListener("change", () => model.updateTile(t.id, { label: label.value }));

    // Список сцен приходит из OBS. Нет соединения с OBS — списка нет, и сцену вводят вручную (при нажатии сервер проверит, что она есть в OBS).
    let scene: HTMLInputElement | HTMLSelectElement;
    if (st.scenes.length === 0) {
      scene = el("input", { type: "text", value: t.action.scene ?? "", placeholder: "Scene name", style: "width:100%" });
    } else {
      const scenes = !t.action.scene || st.scenes.includes(t.action.scene) ? st.scenes : [t.action.scene, ...st.scenes];
      scene = el("select", { style: "width:100%" },
        ...(t.action.scene ? [] : [el("option", { value: "", selected: true }, "— choose a scene —")]),
        ...scenes.map((s) => el("option", { value: s, selected: s === t.action.scene }, s)));
    }
    scene.addEventListener("change", () => model.updateTile(t.id, { scene: scene.value }));
    // «Что делает плитка»: сцена OBS или действие плагина (их объявляют установленные wasm-плагины). Выбор кодируется строкой value.
    const isPlugin = t.action.type === "plugin";
    const isFolder = t.action.type === "folder";
    const current = isPlugin ? `plugin:${t.action.plugin}:${t.action.pluginAction}` : isFolder ? "folder" : "scene";
    const known = st.pluginActions.some((a) => `plugin:${a.plugin}:${a.id}` === current);
    const byPlugin = new Map<string, typeof st.pluginActions>();
    for (const a of st.pluginActions) byPlugin.set(a.plugin, [...(byPlugin.get(a.plugin) ?? []), a]);
    const what = el("select", { style: "width:100%" },
      el("option", { value: "scene", selected: current === "scene" }, "Switch OBS scene"),
      el("option", { value: "folder", selected: isFolder }, "Folder of scenes"),
      ...[...byPlugin.values()].map((list) => el("optgroup", { label: list[0].pluginName || list[0].plugin },
        ...list.map((a) => el("option", { value: `plugin:${a.plugin}:${a.id}`, selected: `plugin:${a.plugin}:${a.id}` === current }, a.title)))),
      // Плагин удалён или отключён: плитка остаётся, но честно говорит, что работать не будет.
      ...(isPlugin && !known ? [el("option", { value: current, selected: true }, `${t.action.plugin}: ${t.action.pluginAction} (plugin not available)`)] : []));
    what.addEventListener("change", () => {
      if (what.value === "folder") { model.updateTile(t.id, { folder: [t.action.scene || st.scenes[0] || ""].filter(Boolean) }); return; }
      if (what.value === "scene") { model.updateTile(t.id, { scene: unusedScenes(layout, st.scenes)[0] ?? st.scenes[0] ?? "" }); return; }
      const a = st.pluginActions.find((x) => `plugin:${x.plugin}:${x.id}` === what.value);
      if (a) model.updateTile(t.id, { pluginAction: a });
    });
    const pluginHint = isPlugin
      ? el("div", { class: "hint" }, known ? "Runs when the plugin is running. A plugin that isn't running makes the tile show an error." : "Install or enable the plugin, or pick another function.")
      : "";
    const sceneHint = st.scenes.length === 0 ? el("div", { class: "hint" }, "The scene list is unavailable (OBS is not connected?). Type the scene name exactly as in OBS.") : "";

    const folderScenes = t.action.scenes ?? [];
    const folderBox = isFolder ? folderEditor(t, folderScenes, st.scenes) : "";

    const colors = el("div", { class: "row", style: "flex-wrap:wrap" });
    for (const [name, hex] of Object.entries(st.palette)) {
      const b = el("button", {
        type: "button", title: name,
        style: `width:26px;height:26px;padding:0;background:${hex};border:2px solid ${t.color === name ? "#fff" : "transparent"};border-radius:4px`,
      });
      b.addEventListener("click", () => model.updateTile(t.id, { color: name }));
      colors.append(b);
    }

    const icon = el("select", { style: "width:100%" }, el("option", { value: "" }, "— none —"),
      ...Object.entries(st.icons).map(([n, g]) => el("option", { value: n, selected: n === t.icon }, `${g} ${n}`)));
    icon.addEventListener("change", () => model.updateTile(t.id, { icon: icon.value }));

    const sizes = el("div", { class: "row", style: "flex-wrap:wrap" });
    const fit = sizesFor(layout, t.id, st.grid);
    for (const s of st.grid.sizes) {
      const ok = fit.some((f) => f.w === s.w && f.h === s.h);
      const b = el("button", { type: "button", class: s.w === t.w && s.h === t.h ? "primary" : "", disabled: !ok, title: ok ? "" : "Doesn't fit here" }, sizeKey(s));
      b.addEventListener("click", () => model.updateTile(t.id, { w: s.w, h: s.h }));
      sizes.append(b);
    }

    const confirmBox = el("input", { type: "checkbox", checked: !!t.confirm });
    confirmBox.addEventListener("change", () => model.updateTile(t.id, { confirm: confirmBox.checked }));

    const del = el("button", { type: "button", class: "danger" }, "Delete tile");
    del.addEventListener("click", () => model.removeTile(t.id));

    return el("div", {},
      el("h3", {}, "Tile"),
      el("label", {}, "Title"), label,
      el("h3", {}, "Function"),
      el("label", {}, "What it does"), what,
      ...(isPlugin ? [pluginHint] : isFolder ? [folderBox] : [el("label", {}, "Scene"), scene, sceneHint]),
      ...(isFolder ? [] : [el("label", { class: "chk", style: "display:flex;gap:6px;align-items:center;margin-top:8px" }, confirmBox, "Ask for a second tap (risky actions)")]),
      el("h3", {}, "Look"),
      el("label", {}, "Size"), sizes,
      el("label", {}, "Color"), colors,
      el("label", {}, "Icon"), icon,
      el("div", { class: "row", style: "margin-top:14px" }, del),
    );
  }

  function render(st: DeckState) {
    refs.saveBtn.disabled = !st.dirty;
    refs.status.textContent = !st.loaded ? (st.error || "Loading…") : st.dirty ? "Unsaved changes" : st.saved ? "Saved" : "Default layout (not saved yet)";
    for (const n of [refs.groupList, refs.sizeBtns, refs.tileList, refs.canvas, refs.props]) n.replaceChildren();
    const l = st.layout;
    if (!l) return;
    const cur = currentGroup(st);

    l.groups.forEach((g) => {
      const item = el("div", { class: "item" + (cur?.id === g.id ? " sel" : "") },
        el("span", { class: "name" }, g.name || "(no name)"), el("span", { class: "hint" }, String(g.tiles.length)));
      item.addEventListener("click", () => { curGroup = g.id; model.select(null); });
      refs.groupList.append(item);
    });

    for (const s of st.grid.sizes) {
      const b = el("button", { type: "button", title: `Add a ${sizeKey(s)} tile` }, `+ ${sizeKey(s)}`);
      b.disabled = !cur;
      b.addEventListener("click", () => {
        if (cur && !model.addTile(cur.id, s)) ui.toast("No room for a tile of this size in the group");
      });
      refs.sizeBtns.append(b);
    }

    for (const t of cur?.tiles ?? []) {
      const item = el("div", { class: "item" + (st.selected === t.id ? " sel" : "") },
        el("span", { style: `width:12px;height:12px;border-radius:2px;background:${st.palette[t.color ?? ""] ?? "#555"}` }),
        el("span", { class: "name" }, tileTitle(t) || "(empty)"), el("span", { class: "hint" }, sizeKey(t)));
      item.addEventListener("click", () => model.select(t.id));
      refs.tileList.append(item);
    }

    const strip = el("div", { style: "display:flex;align-items:flex-start" });
    for (const g of l.groups) strip.append(groupEl(st, g, cur?.id === g.id));
    if (!l.groups.length) strip.append(el("div", { class: "hint" }, "No groups. Add one on the left."));
    refs.canvas.append(strip);

    const sel = st.selected ? findTile(l, st.selected) : null;
    if (sel) refs.props.append(tileProps(st, sel.tile));
    else if (cur) refs.props.append(groupProps(st, cur, l.groups.indexOf(cur), l.groups.length));
  }
  model.subscribe(render);

  function setActive(on: boolean) {
    active = on;
    refs.deckMain.hidden = !on;
    refs.editorMain.hidden = on;
    for (const n of refs.hideInDeck) n.hidden = on;
    refs.saveBtn.hidden = !on; // кнопка сохранения deck — в шапке справа, на месте Save раскладок
    refs.openBtn.textContent = on ? "← Overlay editor" : "▦ Deck";
  }

  addEventListener("keydown", (e) => {
    if (!active) return;
    const t = e.target as HTMLElement;
    const sel = model.get().selected;
    if (!isDeleteTile({ key: e.key, mod: e.ctrlKey || e.metaKey, alt: e.altKey, tag: t.tagName, editable: t.isContentEditable, dialogOpen: !!document.querySelector("dialog[open]") }, sel)) return;
    e.preventDefault();
    if (sel) model.removeTile(sel);
  });

  refs.openBtn.addEventListener("click", async () => {
    if (active) {
      if (model.get().dirty && !confirm("Discard unsaved deck changes?")) return;
      setActive(false);
      return;
    }
    setActive(true);
    try { await model.load(); } catch (e) { ui.fail(e); }
  });

  refs.addGroupBtn.addEventListener("click", () => {
    const id = model.addGroup();
    if (id) curGroup = id; else ui.toast("Too many groups");
    render(model.get());
  });

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

  window.addEventListener("beforeunload", (e) => { if (model.get().dirty) e.preventDefault(); });
  return { isActive: () => active };
}
