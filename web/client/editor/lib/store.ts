// Минимальное реактивное хранилище. Контракт намеренно совпадает с тем, что понимают фреймворки:
//   - Svelte: объект со `subscribe(fn)` — это store (`$store`); здесь fn вызывается сразу и при каждом изменении;
//   - React: `useSyncExternalStore(store.subscribe, store.get)`;
//   - Vue: `shallowRef` + `store.subscribe(v => ref.value = v)`.
// Модели (features/*/model.ts) держат состояние в таком хранилище и не знают про DOM.

export type Unsubscribe = () => void;

/** То, что нужно представлению от модели: читать и подписываться (структурный/«утиный» тип). */
export interface Readable<T> {
  get(): T;
  subscribe(fn: (value: T) => void): Unsubscribe;
}

export interface Store<T> extends Readable<T> {
  set(next: T): void;
  update(fn: (current: T) => T): void;
}

export function createStore<T>(initial: T): Store<T> {
  let value = initial;
  const subs = new Set<(v: T) => void>();
  const store: Store<T> = {
    get: () => value,
    set(next) {
      if (Object.is(next, value)) return;
      value = next;
      for (const f of [...subs]) f(value);
    },
    update(fn) { store.set(fn(value)); },
    subscribe(fn) {
      subs.add(fn);
      fn(value);
      return () => subs.delete(fn);
    },
  };
  return store;
}
