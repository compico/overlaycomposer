// Lock-файл wasm-плагинов: разбор файла и подбор того, что предлагать к установке. Без DOM.
import type { WasmLockFile, WasmLockPlanItem } from "./wasm.ts";

/** Разбор lock-файла из текста; кидает понятную ошибку. Содержимое записей проверяет сервер (план). */
export function parseLockFile(text: string): WasmLockFile {
  let raw: unknown;
  try { raw = JSON.parse(text); } catch { throw new Error("This is not a JSON file."); }
  const o = raw as { version?: unknown; plugins?: unknown } | null;
  if (!o || typeof o !== "object" || !Array.isArray(o.plugins)) throw new Error("This is not a plugins lock file (no “plugins” list).");
  const plugins = o.plugins.filter((p): p is Record<string, unknown> => !!p && typeof p === "object").map((p) => ({
    id: String(p.id ?? ""), repo: String(p.repo ?? ""), tag: String(p.tag ?? ""), version: String(p.version ?? ""),
    sha256: String(p.sha256 ?? ""), ...(typeof p.size === "number" ? { size: p.size } : {}),
  }));
  return { version: typeof o.version === "number" ? o.version : 1, plugins };
}

/** Какие записи плана отмечены по умолчанию: все, что можно поставить. Проблемные и уже установленные — нет. */
export function defaultSelection(items: WasmLockPlanItem[]): Set<string> {
  return new Set(items.filter((i) => i.installable).map((i) => i.id));
}

/** Подпись состояния записи для списка. */
export function statusLabel(i: WasmLockPlanItem): string {
  switch (i.status) {
    case "ok": return "will be installed";
    case "installed": return "already installed";
    case "differs": return `installed ${i.installedVersion ?? "another version"} — restore ${i.version}`;
    case "deprecated": return "deprecated";
    case "removed": return "removed";
    case "changed": return "file changed";
    case "no-source": return "source not added";
    case "unavailable": return "source unavailable";
    case "conflict": return "id is taken";
    default: return "invalid entry";
  }
}

/** Текст о замене устаревшего плагина (или пусто). */
export function replacementText(i: WasmLockPlanItem): string {
  const r = i.deprecation?.replacedBy;
  return r ? `Replacement: ${r.id} (${r.repo})` : "";
}
