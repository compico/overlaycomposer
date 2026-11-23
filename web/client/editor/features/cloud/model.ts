// Облако для файлов настроек (#98/#199): подключённая папка, список файлов, загрузка и скачивание. Без DOM.
import type { Api } from "../../lib/api.ts";

export interface CloudStatus {
  /** "folder" | "google" | "" (не подключено). */
  provider: string;
  folder?: string;
  connected: boolean;
  /** Google Drive: есть ли OAuth-клиент в этой сборке и есть ли сохранённое подключение. */
  google?: { available: boolean; connected: boolean };
}
export interface CloudFile { id: string; name: string; size: number; modTime: string; fingerprint: string }

export interface CloudModel {
  status(): Promise<CloudStatus>;
  connect(folder: string): Promise<CloudStatus>;
  disconnect(): Promise<CloudStatus>;
  /** Адрес страницы согласия Google: его нужно открыть в браузере; подключение завершится само (см. статус). */
  googleStart(): Promise<string>;
  list(): Promise<CloudFile[]>;
  /** Новый файл (name пусто — имя по умолчанию) или замена известной версии (name + expect — её отпечаток). */
  upload(p: { sections: string[]; name?: string; expect?: string }): Promise<CloudFile>;
  /** Исходный текст файла настроек (не разбирается: контрольные суммы). */
  download(id: string): Promise<string>;
  remove(id: string): Promise<void>;
}

export function createCloudModel(api: Api): CloudModel {
  const file = (id: string) => `/api/cloud/files/${encodeURIComponent(id)}`;
  return {
    status: () => api<CloudStatus>("/api/cloud"),
    connect: (folder) => api<CloudStatus>("/api/cloud", "PUT", { folder }),
    disconnect: () => api<CloudStatus>("/api/cloud", "DELETE"),
    googleStart: async () => (await api<{ url: string }>("/api/cloud/google/start", "POST")).url,
    list: async () => (await api<{ files: CloudFile[] }>("/api/cloud/files")).files ?? [],
    upload: ({ sections, name, expect }) => api<CloudFile>("/api/cloud/upload", "POST", { sections, ...(name ? { name } : {}), ...(expect ? { expect } : {}) }),
    download: async (id) => (await api<{ text: string }>(file(id))).text,
    remove: async (id) => { await api(file(id), "DELETE"); },
  };
}

export function formatSize(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / 1024 / 1024).toFixed(1)} MB`;
}

/** Название подключения для интерфейса. */
export function providerLabel(s: CloudStatus): string {
  if (s.provider === "google") return "Google Drive (hidden app folder)";
  if (s.provider === "folder") return `Folder: ${s.folder ?? ""}`;
  return "Not connected";
}

/** Подпись файла в списке: «имя — 12.3 KB, 21.11.2026 11:30». */
export function fileLabel(f: CloudFile): string {
  const d = new Date(f.modTime);
  const when = Number.isNaN(d.getTime()) ? "" : `, ${d.toLocaleString()}`;
  return `${f.name} — ${formatSize(f.size)}${when}`;
}
