// Вертикальные вкладки диалога: кнопки `[data-stab=имя]` переключают панели `[data-spane=имя]` внутри root.
export function mountTabs(root: HTMLElement, onSelect?: (name: string) => void): (name: string) => void {
  const tabs = [...root.querySelectorAll<HTMLElement>("[data-stab]")];
  const panes = [...root.querySelectorAll<HTMLElement>("[data-spane]")];
  const select = (name: string) => {
    for (const t of tabs) {
      const on = t.dataset.stab === name;
      t.classList.toggle("sel", on);
      t.setAttribute("aria-selected", String(on));
    }
    for (const p of panes) p.hidden = p.dataset.spane !== name;
    onSelect?.(name);
  };
  for (const t of tabs) t.addEventListener("click", () => select(t.dataset.stab ?? ""));
  return select;
}
