// Мелкие DOM-помощники ванильных представлений. При переходе на фреймворк эти представления заменяются компонентами,
// а модели и lib/{store,api}.ts остаются как есть.

export const $ = <T extends HTMLElement = HTMLElement>(id: string) => document.getElementById(id) as T;

export function el<K extends keyof HTMLElementTagNameMap>(
  tag: K,
  props: Partial<Omit<HTMLElementTagNameMap[K], "style">> & { class?: string; style?: string } = {},
  ...children: (Node | string)[]
): HTMLElementTagNameMap[K] {
  const e = document.createElement(tag);
  const { class: cls, style, ...rest } = props as Record<string, unknown>;
  if (cls) e.className = cls as string;
  if (style) e.style.cssText = style as string;
  Object.assign(e, rest);
  for (const c of children) e.append(c);
  return e;
}
