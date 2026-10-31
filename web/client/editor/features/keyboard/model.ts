// Горячие клавиши: чистое сопоставление «нажатие + контекст → команда». Без DOM — тестируется в Node.

export type Command =
  | { type: "save" }
  | { type: "undo" }
  | { type: "redo" }
  | { type: "copy" }
  | { type: "paste" }
  | { type: "toggleCompose" }
  | { type: "deselect" }
  | { type: "delete" }
  | { type: "nudge"; dx: number; dy: number };

export interface KeyInput {
  key: string;
  /** Физическая клавиша (KeyboardEvent.code: "KeyZ" …) — не зависит от раскладки. */
  code?: string;
  /** Ctrl или Cmd. */
  mod: boolean;
  alt: boolean;
  shift: boolean;
  /** tagName цели события ("INPUT", "DIV" …). */
  tag: string;
  /** type, если цель — INPUT. */
  inputType?: string;
}

export interface KeyContext {
  hasSelection: boolean;
  selectionLocked: boolean;
  hasClip: boolean;
}

export interface Resolved {
  command: Command;
  /** Подавить действие браузера по умолчанию. */
  prevent: boolean;
}

const NON_TEXT_INPUTS = ["checkbox", "radio", "range", "button"];

/** Поле, где Ctrl+Z — родная отмена текста. */
export function isTyping(tag: string, inputType?: string): boolean {
  return tag === "TEXTAREA" || tag === "SELECT" || (tag === "INPUT" && !NON_TEXT_INPUTS.includes(inputType ?? "text"));
}

/**
 * Буква для сочетания клавиш. Латинская раскладка (включая Dvorak, AZERTY, QWERTZ) — по самой букве, как привыкли пользователи;
 * буква другого алфавита (русская «я», «ы» …) — по физической клавише, чтобы Ctrl+Z/Ctrl+S работали в любой раскладке.
 * Знаки препинания в позиции буквы (`;` в Dvorak на месте Z) не считаются буквой и сочетаний не вызывают.
 */
export function shortcutKey(key: string, code?: string): string {
  const k = key.toLowerCase();
  if (/^[a-z]$/.test(k) || !/^\p{L}$/u.test(k)) return k;
  const m = /^Key([A-Z])$/.exec(code ?? "");
  return m ? m[1].toLowerCase() : k;
}

/** Команда для нажатия или null, если клавиша не наша. */
export function resolveKey(k: KeyInput, ctx: KeyContext): Resolved | null {
  const key = shortcutKey(k.key, k.code);
  if (k.mod && key === "s") return { command: { type: "save" }, prevent: true };

  if (k.mod && !k.alt && !isTyping(k.tag, k.inputType)) {
    if (key === "z" && !k.shift) return { command: { type: "undo" }, prevent: true };
    if ((key === "z" && k.shift) || key === "y") return { command: { type: "redo" }, prevent: true };
  }
  // Остальное — не в полях ввода.
  if (k.tag === "INPUT" || k.tag === "SELECT" || k.tag === "TEXTAREA") return null;

  if (k.mod && !k.alt && key === "c" && ctx.hasSelection) return { command: { type: "copy" }, prevent: true };
  if (k.mod && !k.alt && key === "v" && ctx.hasClip) return { command: { type: "paste" }, prevent: true };
  if (!k.mod && !k.alt && key === "l") return { command: { type: "toggleCompose" }, prevent: true };

  if (!ctx.hasSelection) return null;
  if (k.key === "Escape") return { command: { type: "deselect" }, prevent: false };
  if (k.key === "Delete" || k.key === "Backspace") return { command: { type: "delete" }, prevent: true };

  const step = k.shift ? 10 : 1;
  const moves: Record<string, [number, number]> = {
    ArrowLeft: [-step, 0], ArrowRight: [step, 0], ArrowUp: [0, -step], ArrowDown: [0, step],
  };
  const m = moves[k.key];
  if (m && !ctx.selectionLocked) return { command: { type: "nudge", dx: m[0], dy: m[1] }, prevent: true };
  return null;
}
