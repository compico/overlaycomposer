// История правок для Ctrl+Z / Ctrl+Y: стек снимков (строк) без зависимостей от DOM — тестируется в Node.
// Снимок — любое строковое представление состояния (в редакторе — JSON объектов layout).

export class History {
  #stack: string[];
  #i = 0;
  #saved: string;
  readonly #limit: number;

  constructor(initial: string, limit = 200) {
    this.#stack = [initial];
    this.#saved = initial;
    this.#limit = Math.max(2, limit);
  }

  get current(): string {
    return this.#stack[this.#i];
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
  record(snapshot: string): boolean {
    if (snapshot === this.current) return false;
    this.#stack.length = this.#i + 1;
    this.#stack.push(snapshot);
    if (this.#stack.length > this.#limit) this.#stack.shift();
    this.#i = this.#stack.length - 1;
    return true;
  }

  /** Вернуться на шаг назад; null, если некуда. */
  undo(): string | null {
    if (!this.canUndo) return null;
    return this.#stack[--this.#i];
  }

  redo(): string | null {
    if (!this.canRedo) return null;
    return this.#stack[++this.#i];
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
