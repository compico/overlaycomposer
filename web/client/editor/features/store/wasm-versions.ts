// Список версий для выбора: latest, диапазоны (^2, ^1.5) и точные версии. Без DOM.
export interface WasmVersion { version: string; tag: string }
export interface VersionChoice {
  /** "latest", "^1.5" или точная версия: значение пункта списка */
  value: string;
  label: string;
  version: string;
  tag: string;
}

const MAX_CHOICES = 60;

function parts(v: string): [number, number, number] | null {
  const m = /^(\d+)\.(\d+)\.(\d+)$/.exec(v);
  return m ? [Number(m[1]), Number(m[2]), Number(m[3])] : null;
}

/**
 * Пункты выбора версии по списку выпущенных (от новой к старой, как отдаёт сервер).
 * `latest` — новейшая; `^M` — новейшая в мажоре M (если в нём больше одной версии); `^M.m` — новейшая в минорной ветке (если в ней больше
 * одной версии и она не совпадает с `^M`); дальше все точные версии. Диапазон выбирается один раз: ставится конкретная версия, которую он даёт сейчас.
 */
export function versionChoices(versions: WasmVersion[]): VersionChoice[] {
  const list = versions.filter((v) => parts(v.version));
  if (!list.length) return [];
  const out: VersionChoice[] = [];
  const add = (value: string, label: string, v: WasmVersion) => out.push({ value, label, version: v.version, tag: v.tag });
  add("latest", `latest (${list[0].version})`, list[0]);

  const majors: number[] = [];
  const byMajor = new Map<number, WasmVersion[]>();
  const byMinor = new Map<string, WasmVersion[]>();
  for (const v of list) {
    const [M, m] = parts(v.version)!;
    if (!byMajor.has(M)) { byMajor.set(M, []); majors.push(M); }
    byMajor.get(M)!.push(v);
    const k = `${M}.${m}`;
    if (!byMinor.has(k)) byMinor.set(k, []);
    byMinor.get(k)!.push(v);
  }
  for (const M of majors) {
    const inMajor = byMajor.get(M)!;
    if (inMajor.length > 1) add(`^${M}`, `^${M} (${inMajor[0].version})`, inMajor[0]);
    const seen = new Set<string>();
    for (const v of inMajor) {
      const k = `${M}.${parts(v.version)![1]}`;
      if (seen.has(k)) continue;
      seen.add(k);
      const inMinor = byMinor.get(k)!;
      if (inMinor.length > 1 && inMinor[0].version !== inMajor[0].version) add(`^${k}`, `^${k} (${inMinor[0].version})`, inMinor[0]);
    }
  }
  for (const v of list) add(v.version, v.version, v);
  return out.slice(0, MAX_CHOICES);
}

/** Сравнение версий вида 1.2.3: <0 — a старше b, 0 — равны, >0 — a новее b (нечисловые — как равные). */
export function compareVersions(a: string, b: string): number {
  const x = parts(a), y = parts(b);
  if (!x || !y) return 0;
  for (let i = 0; i < 3; i++) if (x[i] !== y[i]) return x[i] - y[i];
  return 0;
}

export type InstallKind = "install" | "update" | "downgrade" | "same";

/** Что сделает кнопка, если выбрана версия `to` при установленной `installed` (пусто — не установлен), и её подпись. */
export function installAction(installed: string | undefined, to: string): { kind: InstallKind; label: string } {
  if (!installed) return { kind: "install", label: `Install ${to}` };
  const c = compareVersions(to, installed);
  if (to === installed) return { kind: "same", label: "Installed" };
  if (c > 0) return { kind: "update", label: `Update to ${to}` };
  return { kind: "downgrade", label: `Switch to ${to}` };
}
