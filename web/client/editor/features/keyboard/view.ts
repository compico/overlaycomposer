import { type Command, type KeyContext, resolveKey } from "./model.ts";

export interface KeyboardPorts {
  context(): KeyContext;
  run(cmd: Command): void;
}

/** Подписка на keydown окна: сопоставляет нажатие с командой (model) и передаёт её исполнителю. */
export function mountKeyboard(ports: KeyboardPorts) {
  addEventListener("keydown", (e) => {
    const t = e.target as HTMLElement;
    const r = resolveKey(
      { key: e.key, code: e.code, mod: e.ctrlKey || e.metaKey, alt: e.altKey, shift: e.shiftKey, tag: t.tagName, inputType: (t as HTMLInputElement).type },
      ports.context(),
    );
    if (!r) return;
    if (r.prevent) e.preventDefault();
    ports.run(r.command);
  });
}
