// Импорт сцены из OBS: список сцен и сам импорт.
import type { Layout, Source } from "../../../../shared/types.ts";
import type { Api } from "../../lib/api.ts";

export interface ImportReport {
  layout: Layout;
  newSources: Source[];
  reusedSources: number;
  proxied: number;
  skipped: { name: string; reason: string }[];
  warnings: string[];
  bound: boolean;
}

export interface ObsImportModel {
  scenes(): Promise<string[]>;
  import(scene: string, bind: boolean): Promise<ImportReport>;
}

/** Строки отчёта для пользователя (первая — итог). */
export function reportLines(r: ImportReport): string[] {
  return [
    `Imported “${r.layout.name}”: ${r.layout.objects.length} layer(s), ${r.newSources.length} new source(s), ${r.proxied} via proxy` +
      (r.bound ? ", bound to the scene" : ""),
    ...r.skipped.map((x) => `skipped ${x.name}: ${x.reason}`),
    ...r.warnings.map((x) => `warning: ${x}`),
  ];
}

export function createObsImportModel(api: Api): ObsImportModel {
  return {
    scenes: async () => (await api<{ scenes: string[] }>("/api/obs/scenes")).scenes,
    import: (scene, bind) => api<ImportReport>("/api/obs/import", "POST", { scene, bind }),
  };
}
