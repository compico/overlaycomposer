// Серверные (wasm) плагины: список, права, настройки, перезапуск и выбор/создание награды Twitch.
// Модель зависит только от `Api`; текст, пришедший от плагина, показывает представление (через textContent).
import type { Api } from "../../lib/api.ts";
import { createStore, type Readable } from "../../lib/store.ts";

export interface WasmMeta {
  id: string;
  name: string;
  version: string;
  description?: string;
  author?: string;
  permissions?: string[];
  configSchema?: Record<string, unknown>;
}

export interface WasmPlugin {
  meta: WasmMeta;
  file: string;
  state: string;
  lastError?: string;
  restarts: number;
  granted: string[];
  config?: Record<string, unknown>;
}

export interface WasmInfo {
  plugins: WasmPlugin[];
  problems: { file: string; error: string }[];
  dirs: string[];
  twitch: { state: string; detail?: string };
}

export interface Reward {
  id: string;
  title: string;
  cost: number;
  prompt: string;
  inputRequired: boolean;
  enabled: boolean;
  manageable: boolean;
}

export interface NewReward {
  title: string;
  cost: number;
  prompt: string;
  inputRequired: boolean;
}

export interface WasmModel extends Readable<WasmInfo | null> {
  /** null в состоянии — раздел недоступен (403/501): представление его скрывает. */
  load(): Promise<void>;
  /** Заново просканировать папки (новый .wasm без перезапуска OC). */
  reload(): Promise<void>;
  setGrants(id: string, granted: string[]): Promise<void>;
  setConfig(id: string, config: Record<string, unknown>): Promise<void>;
  restart(id: string): Promise<void>;
  rewards(): Promise<Reward[]>;
  createReward(r: NewReward): Promise<Reward>;
}

export function createWasmModel(api: Api): WasmModel {
  const state = createStore<WasmInfo | null>(null);
  const apply = (v: WasmInfo) => state.set({ ...v, plugins: v.plugins ?? [], problems: v.problems ?? [], dirs: v.dirs ?? [] });
  const enc = encodeURIComponent;
  return {
    get: state.get,
    subscribe: state.subscribe,
    async load() {
      try { apply(await api<WasmInfo>("/api/wasmplugins")); } catch { state.set(null); }
    },
    async reload() { apply(await api<WasmInfo>("/api/wasmplugins/reload", "POST", {})); },
    async setGrants(id, granted) { apply(await api<WasmInfo>(`/api/wasmplugins/${enc(id)}/grants`, "PUT", { granted })); },
    async setConfig(id, config) { apply(await api<WasmInfo>(`/api/wasmplugins/${enc(id)}/config`, "PUT", config)); },
    async restart(id) { apply(await api<WasmInfo>(`/api/wasmplugins/${enc(id)}/restart`, "POST", {})); },
    async rewards() {
      const r = await api<{ rewards: Reward[] | null }>("/api/twitch/rewards");
      return sortRewards(r.rewards ?? []);
    },
    createReward: (r) => api<Reward>("/api/twitch/rewards", "POST", r),
  };
}

// ---- чистая логика представления ----

const PERMISSION_TEXT: Record<string, string> = {
  storage: "Keep its own data (queue, settings) in Overlay Composer",
  "net.http": "Make requests to the internet (never to your local network)",
  "twitch.redemption": "See channel point redemptions and mark them fulfilled or cancelled",
  timer: "Run on a timer in the background",
};

/** Что даёт право, по-человечески; неизвестное право показывается как есть. */
export const permissionText = (p: string): string => PERMISSION_TEXT[p] ?? p;

/** Награды, которыми приложение может управлять, — первыми: только у них меняется статус погашения. */
export function sortRewards(list: Reward[]): Reward[] {
  return [...list].sort((a, b) => Number(b.manageable) - Number(a.manageable) || a.title.localeCompare(b.title));
}

/** Права, запрошенные плагином, но не выданные. */
export function missingGrants(p: WasmPlugin): string[] {
  const granted = new Set(p.granted);
  return (p.meta.permissions ?? []).filter((x) => !granted.has(x));
}

/** Плагину нужно внимание пользователя: не выданы права или он остановлен. */
export const needsAttention = (p: WasmPlugin): boolean => missingGrants(p).length > 0 || p.state === "failed";

/** Новый набор прав после переключения флажка; порядок — как в запросе плагина, лишних прав нет. */
export function toggleGrant(p: WasmPlugin, perm: string, on: boolean): string[] {
  const set = new Set(p.granted);
  if (on) set.add(perm); else set.delete(perm);
  const known = p.meta.permissions ?? [];
  return known.filter((x) => set.has(x));
}

export type FieldKind = "text" | "number" | "boolean" | "reward";

export interface ConfigField {
  key: string;
  kind: FieldKind;
  title: string;
  description: string;
  integer: boolean;
}

/** Плоская JSON-схема плагина → поля формы. Неподдерживаемые типы пропускаются (их можно задать только через API). */
export function configFields(schema: Record<string, unknown> | undefined): ConfigField[] {
  if (!schema) return [];
  const out: ConfigField[] = [];
  for (const key of Object.keys(schema).sort()) {
    const f = schema[key];
    if (!f || typeof f !== "object") continue;
    const o = f as Record<string, unknown>;
    const type = typeof o.type === "string" ? o.type : "string";
    let kind: FieldKind;
    if (type === "string") kind = o.format === "twitch.reward" ? "reward" : "text";
    else if (type === "number" || type === "integer") kind = "number";
    else if (type === "boolean") kind = "boolean";
    else continue;
    out.push({
      key, kind, integer: type === "integer",
      title: typeof o.title === "string" && o.title ? o.title : key,
      description: typeof o.description === "string" ? o.description : "",
    });
  }
  return out;
}

/** Значения формы (строки/флажки) → объект настроек. Пустые поля не попадают в объект; ошибка — понятный текст. */
export function buildConfig(fields: ConfigField[], values: Record<string, string | boolean>): { config?: Record<string, unknown>; error?: string } {
  const config: Record<string, unknown> = {};
  for (const f of fields) {
    const v = values[f.key];
    if (f.kind === "boolean") { config[f.key] = v === true; continue; }
    const s = typeof v === "string" ? v.trim() : "";
    if (s === "") continue;
    if (f.kind === "number") {
      const n = Number(s);
      if (!Number.isFinite(n)) return { error: `${f.title}: a number is expected` };
      if (f.integer && !Number.isInteger(n)) return { error: `${f.title}: a whole number is expected` };
      config[f.key] = n;
    } else {
      config[f.key] = s;
    }
  }
  return { config };
}

/** Текущие настройки плагина → значения формы. */
export function formValues(fields: ConfigField[], config: Record<string, unknown> | undefined): Record<string, string | boolean> {
  const out: Record<string, string | boolean> = {};
  for (const f of fields) {
    const v = config?.[f.key];
    out[f.key] = f.kind === "boolean" ? v === true : v === undefined || v === null ? "" : String(v);
  }
  return out;
}

/** Проверка формы создания награды до запроса (сервер проверяет ещё раз). */
export function validateNewReward(r: NewReward): string {
  if (!r.title.trim()) return "Enter a reward title";
  if (!Number.isInteger(r.cost) || r.cost < 1) return "Cost must be a whole number of at least 1";
  return "";
}

/** Строка состояния EventSub для шапки окна. */
export function twitchLine(t: WasmInfo["twitch"] | undefined, anyWants: boolean): string {
  if (!t) return "";
  if (!anyWants) return "Twitch: not needed by any plugin";
  return t.detail ? `Twitch: ${t.state} — ${t.detail}` : `Twitch: ${t.state}`;
}
