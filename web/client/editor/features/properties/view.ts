import type { LayoutObject, PluginManifest, RouterMode, Source, Transform } from "../../../../shared/types.ts";
import { COPY_ICON, copyText } from "../../lib/clipboard.ts";
import { el } from "../../lib/dom.ts";
import { maskUrl } from "../add-source/model.ts";
import type { EditorState } from "../editor/model.ts";
import { canvasFromInputs, type CropSide, resetCrop, ROUTER_MODES, setCrop, transformFields, type PropertiesModel } from "./model.ts";

export interface PropertiesPorts {
  state(): Pick<EditorState, "layout" | "selectedId" | "sources" | "plugins" | "defaultLayoutId">;
  /** Слой правили из поля: пометить несохранённым и перерисовать канвас. */
  fieldEdited(label: string): void;
  /** Слой изменён кнопкой (сброс обрезки): записать и перерисовать. */
  touched(o: LayoutObject, label: string): void;
  /** Имя layout изменено. */
  layoutRenamed(name: string): void;
  /** Размер холста изменён (уже записан в layout). */
  canvasChanged(): void;
  removeObject(id: string): void;
  /** Источник удалён на сервере: перечитать и убрать его слои. */
  sourceDeleted(id: string): Promise<void>;
  /** Default layout сменился на сервере. */
  defaultChanged(): Promise<void>;
  /** Форма настроек плагина (features/plugin-settings). */
  pluginForm(src: Source, m: PluginManifest): HTMLElement;
  /** Блок привязки к сценам: перерисовать после создания его контейнера (#sceneBox). */
  renderSceneBox(): void;
  toast(msg: string): void;
  fail(e: unknown): void;
  confirm(text: string): boolean;
}

/** Панель свойств: выбранный слой (источник, трансформация, обрезка) и настройки layout. */
export function mountProperties(root: HTMLElement, model: PropertiesModel, ports: PropertiesPorts) {
  /** Подтягивают значения полей после изменения слоя жестом на канвасе. */
  let syncers: (() => void)[] = [];
  const sync = () => syncers.forEach((f) => f());

  function numField(label: string, get: () => number, set: (v: number) => void, step: number): HTMLLabelElement {
    const input = el("input", { type: "number" });
    input.step = String(step);
    const paint = () => {
      if (document.activeElement !== input) input.value = String(Math.round(get() * 100) / 100);
    };
    paint();
    syncers.push(paint);
    input.addEventListener("input", () => {
      const v = parseFloat(input.value);
      if (Number.isNaN(v)) return;
      set(v);
      ports.fieldEdited(`Set ${label}`);
      sync();
    });
    return el("label", {}, label, input);
  }

  function cropField(label: string, t: Transform, side: CropSide): HTMLLabelElement {
    return numField(label, () => t[side] ?? 0, (v) => setCrop(t, side, v), 1);
  }

  function nameRow(src: Source): HTMLElement {
    const name = el("input", { type: "text", value: src.name });
    name.addEventListener("change", () => model.renameSource(src, name.value).catch(ports.fail));
    return el("div", { class: "row" }, name);
  }

  function sourceBlock(src: Source) {
    root.append(el("h3", { textContent: "Source" }));
    if (src.type === "plugin") {
      root.append(nameRow(src));
      const m = ports.state().plugins.find((p) => p.id === src.data.plugin);
      if (m) {
        root.append(el("div", { class: "hint", textContent: `${m.name}${m.version ? ` ${m.version}` : ""}${m.builtin ? "" : " · custom plugin"}` }));
        root.append(ports.pluginForm(src, m));
      } else {
        root.append(el("div", { class: "hint warn", textContent: `Plugin “${src.data.plugin}” is not installed — the widget will be empty. Put it back into the plugins folder.` }));
      }
      return;
    }
    // В адресе виджета часто лежит токен: всё после «?»/«#» скрыто, поле открывается на редактирование двойным кликом,
    // а скопировать адрес целиком можно кнопкой рядом.
    let curUrl = src.data.url ?? "";
    const url = el("input", { type: "text" });
    const paintUrl = () => {
      const masked = maskUrl(curUrl) !== curUrl;
      url.value = masked ? maskUrl(curUrl) : curUrl;
      url.readOnly = masked;
      url.title = masked ? "Part of the address is hidden. Double-click to edit; the button copies the full address." : "";
    };
    paintUrl();
    url.addEventListener("dblclick", () => {
      if (!url.readOnly) return;
      url.readOnly = false;
      url.value = curUrl;
      url.focus();
    });
    url.addEventListener("change", () => {
      if (url.readOnly) return;
      curUrl = url.value;
      model.setSourceUrl(src, curUrl).catch(ports.fail);
    });
    url.addEventListener("blur", paintUrl);
    const copyUrl = el("button", { type: "button", class: "iconbtn", title: "Copy the full address", ariaLabel: "Copy the full address" });
    copyUrl.innerHTML = COPY_ICON; // статическая разметка без пользовательских данных
    copyUrl.addEventListener("click", async () => {
      ports.toast((await copyText(curUrl)) ? "Address copied" : "Could not copy — select the address manually");
    });
    const proxyChk = el("input", { type: "checkbox", checked: !!src.data.proxy });
    proxyChk.addEventListener("change", () =>
      model.setSourceProxy(src, proxyChk.checked).catch((e) => {
        proxyChk.checked = !proxyChk.checked; // сервер отказал (например, относительный URL)
        ports.fail(e);
      }));
    // Для сайтов-SPA, у которых после загрузки белая страница: их роутер не знает про префикс /proxy/<id>.
    const router = el("select", {}, ...ROUTER_MODES.map((m) => el("option", { value: m.value, textContent: m.label, selected: m.value === (src.data.router ?? "") })));
    router.addEventListener("change", () =>
      model.setSourceRouter(src, router.value as RouterMode).catch((e) => {
        router.value = src.data.router ?? "";
        ports.fail(e);
      }));
    const routerRow = el("label", { title: "Hides the /proxy/<id> prefix from the site's own router. Turn On if the proxied page stays blank after loading; Auto does it only for known sites." }, "SPA router fix ", router);
    const syncRouter = () => { routerRow.hidden = !proxyChk.checked; };
    proxyChk.addEventListener("change", syncRouter);
    syncRouter();
    root.append(
      nameRow(src),
      el("div", { class: "row" }, url, copyUrl),
      el("label", { class: "chk", title: "For sites that refuse to be embedded (X-Frame-Options)" }, proxyChk, "Load via server proxy"),
      routerRow,
    );
  }

  function objectBlock(o: LayoutObject, src: Source | undefined) {
    const t = o.transform;
    if (src) sourceBlock(src);

    root.append(el("h3", { textContent: "Transform" }));
    root.append(el("div", { class: "grid2" }, ...transformFields(o).map((f) => numField(f.label, f.get, f.set, f.step))));

    root.append(el("h3", { textContent: "Crop (px)" }));
    const cropGrid = el("div", { class: "grid2" },
      cropField("Left", t, "cropLeft"), cropField("Right", t, "cropRight"),
      cropField("Top", t, "cropTop"), cropField("Bottom", t, "cropBottom"));
    const reset = el("button", { textContent: "Reset crop" });
    reset.addEventListener("click", () => {
      resetCrop(t);
      ports.touched(o, "Reset crop");
      render();
    });
    root.append(cropGrid, el("div", { class: "row" }, reset), el("div", { class: "empty", textContent: "Alt+drag a handle crops, like in OBS." }));

    const rm = el("button", { textContent: "Remove from layout" });
    rm.addEventListener("click", () => ports.removeObject(o.id));
    const delSrc = el("button", { class: "danger", textContent: "Delete source" });
    delSrc.addEventListener("click", async () => {
      if (!src || !ports.confirm(`Delete source "${src.name}" from ALL layouts?`)) return;
      try {
        await model.deleteSource(src);
        await ports.sourceDeleted(src.id);
      } catch (e) { ports.fail(e); }
    });
    root.append(el("div", { class: "row", style: "margin-top:10px" }, rm, delSrc));
  }

  function layoutBlock() {
    const st = ports.state();
    const layout = st.layout!;
    root.append(el("h3", { textContent: "Layout" }));
    const lname = el("input", { type: "text", value: layout.name });
    lname.addEventListener("input", () => ports.layoutRenamed(lname.value));
    const isDefault = el("input", { type: "checkbox", checked: layout.id === st.defaultLayoutId });
    isDefault.addEventListener("change", async () => {
      try {
        await model.setDefaultLayout(isDefault.checked ? layout.id : null);
        await ports.defaultChanged();
        ports.toast(isDefault.checked ? "Default layout set" : "No default layout");
      } catch (e) { isDefault.checked = !isDefault.checked; ports.fail(e); }
    });
    const cw = el("input", { type: "number", value: String(layout.canvas.width) });
    const ch = el("input", { type: "number", value: String(layout.canvas.height) });
    const canvasInput = () => {
      layout.canvas = canvasFromInputs(cw.value, ch.value);
      ports.canvasChanged();
    };
    cw.addEventListener("input", canvasInput);
    ch.addEventListener("input", canvasInput);
    root.append(
      el("label", {}, "Name", lname),
      el("label", { class: "chk", style: "margin-top:6px" }, isDefault, " Default layout (for scenes without their own)"),
      el("div", { id: "sceneBox", style: "margin-top:6px" }),
      el("div", { class: "grid2", style: "margin-top:6px" }, el("label", {}, "Canvas W", cw), el("label", {}, "Canvas H", ch)),
      el("div", { class: "empty", textContent: "Shift while resizing keeps aspect; Shift while rotating snaps to 15°. Arrows nudge (Shift = 10px)." }),
    );
    ports.renderSceneBox();
  }

  function render() {
    root.replaceChildren();
    syncers = [];
    const st = ports.state();
    if (!st.layout) return;
    const o = st.layout.objects.find((x) => x.id === st.selectedId);
    if (o) objectBlock(o, st.sources.find((s) => s.id === o.sourceId));
    layoutBlock();
  }

  return { render, sync };
}

