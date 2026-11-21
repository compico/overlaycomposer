// Экспорт и импорт настроек одним файлом (#98): план импорта, выбор элементов, отчёт. Без DOM.
import { type Api, RawJson } from "../../lib/api.ts";

export type ItemStatus = "new" | "replace" | "same" | "problem";
export interface BundleItem {
  id: string;
  name?: string;
  status: ItemStatus;
  reason?: string;
  warning?: boolean;
  /** Не отмечен по умолчанию. */
  optional?: boolean;
}
export interface SectionPlan {
  name: string;
  /** false — эта версия OC секции не знает: она будет пропущена. */
  known: boolean;
  items?: BundleItem[];
  error?: string;
  present: boolean;
}
export interface PlanResponse { format: number; app?: string; createdAt: string; sections: SectionPlan[] }
export interface Outcome { id: string; ok: boolean; error?: string; note?: string }
export interface SectionReport { name: string; outcomes?: Outcome[]; error?: string }
/** секция → id элемента → режим. */
export type Selection = Record<string, Record<string, "replace" | "copy">>;

export interface SectionInfo { name: string; title: string; hint?: string; optional?: boolean; copyable?: boolean }

/** Секции в порядке показа (порядок применения задаёт сервер). */
export const SECTIONS: SectionInfo[] = [
  { name: "layouts", title: "Layouts", copyable: true },
  { name: "sources", title: "Sources", hint: "Permissions granted to plugin widgets are not transferred." },
  { name: "deck", title: "Deck panel" },
  { name: "plugins", title: "Standalone plugins", hint: "Exact versions (lock) and, if the downloaded file matches, saved permissions." },
  { name: "plugin-settings", title: "Plugin settings", optional: true, hint: "May contain tokens and addresses — not included unless you tick it." },
  { name: "store", title: "Plugin sources (store)", hint: "Repositories added to the store; adding one means trusting its plugins." },
  { name: "app", title: "OBS connection", hint: "Without the password." },
];

export const sectionTitle = (name: string): string => SECTIONS.find((s) => s.name === name)?.title ?? name;
export const isCopyable = (name: string): boolean => SECTIONS.find((s) => s.name === name)?.copyable === true;

/** Элемент можно выбрать: не problem и не same (как на сервере). */
export const selectable = (i: BundleItem): boolean => i.status === "new" || i.status === "replace";

/** Что отмечено по умолчанию: все применимые, кроме необязательных. */
export function defaultSelection(plan: SectionPlan[]): Selection {
  const sel: Selection = {};
  for (const s of plan) {
    if (!s.known) continue;
    for (const i of s.items ?? []) {
      if (selectable(i) && !i.optional) (sel[s.name] ??= {})[i.id] = "replace";
    }
  }
  return sel;
}

export const selectedCount = (sel: Selection): number => Object.values(sel).reduce((n, m) => n + Object.keys(m).length, 0);

export const STATUS_LABEL: Record<ItemStatus, string> = { new: "new", replace: "replaces yours", same: "already the same", problem: "cannot import" };

/** Строки отчёта о применении. */
export function reportLines(reports: SectionReport[]): string[] {
  const out: string[] = [];
  for (const r of reports) {
    if (r.error) out.push(`${sectionTitle(r.name)}: ${r.error}`);
    for (const o of r.outcomes ?? []) {
      out.push(`${sectionTitle(r.name)} · ${o.id}: ${o.ok ? "done" : `failed — ${o.error ?? "unknown error"}`}${o.note ? ` (${o.note})` : ""}`);
    }
  }
  return out;
}

export interface BundleModel {
  /** Ссылка на скачивание файла настроек из выбранных секций. */
  exportUrl(sections: string[]): string;
  /** Проверить файл (его исходный текст) и оценить по секциям. Ничего не меняет. */
  plan(text: string): Promise<PlanResponse>;
  apply(text: string, selection: Selection): Promise<SectionReport[]>;
}

export function createBundleModel(api: Api): BundleModel {
  return {
    exportUrl: (sections) => `/api/settings/export?download=1&sections=${encodeURIComponent(sections.join(","))}`,
    plan: (text) => api<PlanResponse>("/api/settings/import/plan", "POST", new RawJson(text)),
    // файл вставляется в тело как есть: разбор и повторная сборка изменили бы байты и сломали контрольные суммы
    apply: async (text, selection) =>
      (await api<{ reports: SectionReport[] }>("/api/settings/import/apply", "POST", new RawJson(`{"file":${text},"selection":${JSON.stringify(selection)}}`))).reports ?? [],
  };
}
