// Режим компоновки и прилипание: два флага, которые помнятся между запусками. Без DOM; хранилище — порт.
import { createStore, type Readable } from "../../lib/store.ts";

export interface PrefStorage {
  get(key: string): string | null;
  set(key: string, value: string): void;
}

export interface ViewPrefs {
  /** Тонкие очертания и подписи всех виджетов. */
  compose: boolean;
  /** Прилипание к краям других виджетов и холста. */
  snap: boolean;
}

export interface ViewPrefsModel extends Readable<ViewPrefs> {
  toggleCompose(): void;
  toggleSnap(): void;
}

const PREFIX = "oc.";

function readFlag(storage: PrefStorage, key: string, def: boolean): boolean {
  try {
    const v = storage.get(PREFIX + key);
    return v === null ? def : v === "1";
  } catch { return def; }
}

function writeFlag(storage: PrefStorage, key: string, v: boolean) {
  try { storage.set(PREFIX + key, v ? "1" : "0"); } catch { /* хранилище недоступно */ }
}

export function createViewPrefsModel(storage: PrefStorage): ViewPrefsModel {
  const store = createStore<ViewPrefs>({ compose: readFlag(storage, "compose", false), snap: readFlag(storage, "snap", true) });
  return {
    get: store.get,
    subscribe: store.subscribe,
    toggleCompose() {
      const compose = !store.get().compose;
      writeFlag(storage, "compose", compose);
      store.update((s) => ({ ...s, compose }));
    },
    toggleSnap() {
      const snap = !store.get().snap;
      writeFlag(storage, "snap", snap);
      store.update((s) => ({ ...s, snap }));
    },
  };
}
