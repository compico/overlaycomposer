// Что представлениям нужно от «окружения» редактора. Интерфейс утиный: фреймворк-версия реализует его по-своему.

export interface Ui {
  toast(msg: string): void;
  fail(e: unknown): void;
}
