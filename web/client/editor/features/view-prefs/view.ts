import type { ViewPrefsModel } from "./model.ts";

export interface ViewPrefsRefs {
  frame: HTMLElement;
  composeBtn: HTMLElement;
  snapBtn: HTMLElement;
}

export interface ViewPrefsPorts {
  /** Режим компоновки переключён: перерисовать рамки. */
  composeChanged(): void;
  toast(msg: string): void;
}

/** Кнопки «Layout» и «Snap» в шапке канваса. */
export function mountViewPrefs(refs: ViewPrefsRefs, model: ViewPrefsModel, ports: ViewPrefsPorts) {
  let last = model.get();
  model.subscribe((s) => {
    refs.frame.classList.toggle("compose", s.compose);
    refs.composeBtn.setAttribute("aria-pressed", String(s.compose));
    refs.snapBtn.setAttribute("aria-pressed", String(s.snap));
    if (s.compose !== last.compose) ports.composeChanged();
    if (s.snap !== last.snap) ports.toast(s.snap ? "Snapping on" : "Snapping off");
    last = s;
  });
  refs.composeBtn.addEventListener("click", () => model.toggleCompose());
  refs.snapBtn.addEventListener("click", () => model.toggleSnap());
}
