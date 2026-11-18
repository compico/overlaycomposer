// Блок «плагин устарел» с кнопкой перехода на замену — общий для магазина, окна wasm-плагинов и центра уведомлений.
import { el } from "../../lib/dom.ts";
import { alternativeFor, deprecationText, type Deprecation } from "./deprecated.ts";
import type { StoreEntry, StoreSource } from "./model.ts";

export interface DeprecationActions {
  /** Поставить замену-виджет из каталога (в окне, где магазина нет, — открыть магазин). */
  install(entry: StoreEntry): void | Promise<unknown>;
}

/** https-ссылка открывается в новой вкладке без доступа к окну редактора. */
export function openExternal(url: string): void {
  if (/^https:\/\//.test(url)) window.open(url, "_blank", "noopener,noreferrer");
}

export function deprecationBlock(dep: Deprecation, sources: StoreSource[], actions: DeprecationActions): HTMLElement {
  const box = el("div", { class: "hint warn", style: "margin-top:4px" }, el("div", { textContent: deprecationText(dep) }));
  const alt = alternativeFor(dep, sources);
  if (!alt) return box;
  if (alt.type === "installed") {
    box.append(el("div", { textContent: alt.label }));
    return box;
  }
  const b = el("button", { type: "button", class: "primary", style: "margin-top:4px", textContent: alt.label });
  if (alt.type === "install") b.addEventListener("click", () => { b.disabled = true; void Promise.resolve(actions.install(alt.entry)).finally(() => { b.disabled = false; }); });
  else {
    b.title = alt.url;
    b.addEventListener("click", () => openExternal(alt.url));
  }
  box.append(b);
  return box;
}
