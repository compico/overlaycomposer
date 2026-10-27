import assert from "node:assert/strict";
import { History } from "../../shared/history.ts";
import type { Steps, World } from "../run.ts";

// Мини-модель редактора: layout как JSON-снимок, историю ведёт History — так же, как в editor/main.ts.
const snap = (w: World) => JSON.stringify(w.layout);
const widget = (w: World, name: string) => {
  const o = w.layout.objects.find((x: any) => x.name === name);
  assert.ok(o, `нет виджета «${name}»`);
  return o;
};
const restore = (w: World, s: string | null) => { if (s !== null) w.layout = JSON.parse(s); };

export function register(s: Steps) {
  s.step("история помнит не более {int} шагов", (w, n) => { w.limit = n; });
  s.step('открыт layout, в котором виджет {string} стоит в x={int}', (w, name, x) => {
    w.layout = { objects: [{ name, transform: { x } }] };
    w.history = new History(snap(w), w.limit ?? 200);
  });
  s.step('я перемещаю виджет {string} в x={int} и фиксирую правку', (w, name, x) => {
    widget(w, name).transform.x = x;
    w.history.record(snap(w));
  });
  s.step("я отменяю правку", (w) => restore(w, w.history.undo()));
  s.step("я повторяю правку", (w) => restore(w, w.history.redo()));
  s.step("я сохраняю layout", (w) => w.history.markSaved());
  s.step('виджет {string} стоит в x={int}', (w, name, x) => assert.equal(widget(w, name).transform.x, x));
  s.step("повтор недоступен", (w) => assert.equal(w.history.canRedo, false));
  s.step("отмена недоступна", (w) => assert.equal(w.history.canUndo, false));
  s.step("есть несохранённые изменения", (w) => assert.equal(w.history.dirty, true));
  s.step("несохранённых изменений нет", (w) => assert.equal(w.history.dirty, false));
  s.step("можно отменить ровно {int} шага", (w, n) => assert.equal(w.history.undoDepth, n));
  s.step("можно отменить ровно {int} шагов", (w, n) => assert.equal(w.history.undoDepth, n));
}
