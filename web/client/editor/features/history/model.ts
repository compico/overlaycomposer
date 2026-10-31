// Панель «History» (список шагов правок, переход к любому — как в FL Studio). Чистые функции строк; сам переход — editor.jumpTo.
import type { HistoryEntry } from "../../../../shared/history.ts";

export interface HistoryRow extends HistoryEntry {
  /** Можно ли перейти сюда (не текущий шаг). */
  canJump: boolean;
  /** Подсказка: сколько шагов до этого состояния и в какую сторону. */
  hint: string;
}

/** Строки панели: новые шаги сверху, самое старое состояние («Opened») внизу. */
export function historyRows(entries: HistoryEntry[]): HistoryRow[] {
  const cur = entries.findIndex((e) => e.current);
  return entries
    .map((e) => {
      const d = e.index - cur;
      return {
        ...e,
        canJump: !e.current,
        hint: e.current ? "Current state" : d < 0 ? `Go back ${-d} step${d === -1 ? "" : "s"}` : `Go forward ${d} step${d === 1 ? "" : "s"}`,
      };
    })
    .reverse();
}

/** Подпись кнопки: сколько шагов можно отменить / повторить. */
export function historySummary(entries: HistoryEntry[]): string {
  const cur = entries.findIndex((e) => e.current);
  if (cur < 0) return "No history";
  const redo = entries.length - 1 - cur;
  return `History — ${cur} to undo${redo ? `, ${redo} to redo` : ""}`;
}
