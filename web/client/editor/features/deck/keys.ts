// Клавиша удаления выбранной плитки deck: чистая логика без DOM.
export interface DeckKey { key: string; mod: boolean; alt: boolean; tag: string; editable: boolean; dialogOpen: boolean }

/** Delete/Backspace удаляет выбранную плитку, если фокус не в поле ввода и не открыт диалог. */
export function isDeleteTile(k: DeckKey, selected: string | null): boolean {
  if (!selected || k.mod || k.alt || k.dialogOpen || k.editable) return false;
  if (k.tag === "INPUT" || k.tag === "TEXTAREA" || k.tag === "SELECT") return false;
  return k.key === "Delete" || k.key === "Backspace";
}
