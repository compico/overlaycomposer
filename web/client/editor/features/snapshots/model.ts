// Локальные снимки настроек (#228): автоснимок при закрытии, ручной снимок, история. Без DOM.
import type { Api } from "../../lib/api.ts";

export type SnapshotKind = "auto" | "manual" | "import";
export interface Snapshot { name: string; kind: SnapshotKind; size: number; modTime: string }
export interface SnapshotsInfo { auto: boolean; max: number; files: Snapshot[] }

export const MAX_LIMIT = 200;

export interface SnapshotsModel {
  info(): Promise<SnapshotsInfo>;
  save(p: { auto?: boolean; max?: number }): Promise<SnapshotsInfo>;
  /** Снимок настроек вручную. */
  snapshot(): Promise<SnapshotsInfo>;
  /** Исходный текст снимка (не разбирается: контрольные суммы). */
  read(name: string): Promise<string>;
  remove(name: string): Promise<SnapshotsInfo>;
}

export function createSnapshotsModel(api: Api): SnapshotsModel {
  const file = (name: string) => `/api/settings/backup/files/${encodeURIComponent(name)}`;
  return {
    info: () => api<SnapshotsInfo>("/api/settings/backup"),
    save: (p) => api<SnapshotsInfo>("/api/settings/backup", "PUT", p),
    snapshot: () => api<SnapshotsInfo>("/api/settings/backup/snapshot", "POST"),
    read: async (name) => (await api<{ text: string }>(file(name))).text,
    remove: (name) => api<SnapshotsInfo>(file(name), "DELETE"),
  };
}

export function kindLabel(k: SnapshotKind): string {
  return k === "auto" ? "on close" : k === "manual" ? "manual" : "before import";
}

/** Значение поля «Keep at most»: целое от 1 до MAX_LIMIT, иначе null. */
export function parseLimit(v: string): number | null {
  const n = Number(v.trim());
  return Number.isInteger(n) && n >= 1 && n <= MAX_LIMIT ? n : null;
}

/** Подпись снимка: «21.11.2026 19:00 — on close, 12.3 KB». */
export function snapshotLabel(s: Snapshot, size: (n: number) => string): string {
  const d = new Date(s.modTime);
  const when = Number.isNaN(d.getTime()) ? s.name : d.toLocaleString();
  return `${when} — ${kindLabel(s.kind)}, ${size(s.size)}`;
}
