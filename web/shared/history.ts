// История правок для Ctrl+Z / Ctrl+Y: стек снимков (строк) без зависимостей от DOM — тестируется в Node.
// Снимок — любое строковое представление состояния (в редакторе — JSON объектов layout).

/** Шаг истории для списка: подпись, это текущее состояние, это «будущее» (после отмены), совпадает с сохранённым. */
export interface HistoryEntry {
  index: number;
  label: string;
  current: boolean;
  future: boolean;
  saved: boolean;
}

export class History {
  #stack: { s: string; label: string }[];
  #i = 0;
  #saved: string;
  #version = 0;
  readonly #limit: number;

  constructor(initial: string, limit = 200, initialLabel = "Opened") {
    this.#stack = [{ s: initial, label: initialLabel }];
    this.#saved = initial;
    this.#limit = Math.max(2, limit);
  }

  /** Растёт при любом изменении списка или положения в нём — по нему представление понимает, что пора перерисовать. */
  get version(): number {
    return this.#version;
  }

  /** Положение в списке (0 — самое старое состояние). */
  get index(): number {
    return this.#i;
  }

  /** Все шаги от старого к новому; для панели «History». */
  get entries(): HistoryEntry[] {
    return this.#stack.map((e, index) => ({ index, label: e.label, current: index === this.#i, future: index > this.#i, saved: e.s === this.#saved }));
  }

  get current(): string {
    return this.#stack[this.#i].s;
  }

  get canUndo(): boolean {
    return this.#i > 0;
  }

  get canRedo(): boolean {
    return this.#i < this.#stack.length - 1;
  }

  /** Число шагов, которые можно отменить. */
  get undoDepth(): number {
    return this.#i;
  }

  /** Запомнить новое состояние. Совпадающее с текущим игнорируется; новая правка после отмены стирает «будущее». */
  record(snapshot: string, label = "Edit"): boolean {
    if (snapshot === this.current) return false;
    this.#stack.length = this.#i + 1;
    this.#stack.push({ s: snapshot, label });
    if (this.#stack.length > this.#limit) this.#stack.shift();
    this.#i = this.#stack.length - 1;
    this.#version++;
    return true;
  }

  /** Вернуться на шаг назад; null, если некуда. */
  undo(): string | null {
    if (!this.canUndo) return null;
    this.#version++;
    return this.#stack[--this.#i].s;
  }

  redo(): string | null {
    if (!this.canRedo) return null;
    this.#version++;
    return this.#stack[++this.#i].s;
  }

  /** Перейти к шагу `index` (как в списке истории FL Studio); null, если такого шага нет или он уже текущий. */
  jumpTo(index: number): string | null {
    if (!Number.isInteger(index) || index < 0 || index >= this.#stack.length || index === this.#i) return null;
    this.#i = index;
    this.#version++;
    return this.#stack[index].s;
  }

  /** Текущее состояние сохранено (кнопка Save). */
  markSaved(): void {
    this.#saved = this.current;
  }

  /** Есть ли несохранённые изменения: после отмены до сохранённого состояния — нет. */
  get dirty(): boolean {
    return this.current !== this.#saved;
  }
}
