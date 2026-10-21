/** Протокол обмена между плагином (iframe) и страницей OC по postMessage. Общий для sdk/oc.ts и client/shared/pluginhost.ts. */

export interface PluginInfo {
  pluginId: string;
  sourceId: string;
  settings: Record<string, string | number | boolean>;
  permissions: string[];
  preview: boolean;
}

/** plugin → host */
export type ToHost =
  | { oc: 1; t: "hello" }
  | { oc: 1; t: "call"; id: number; method: string; params?: unknown };

/** host → plugin */
export type ToPlugin =
  | { oc: 1; t: "ready"; info: PluginInfo }
  | { oc: 1; t: "reply"; id: number; ok: true; result?: unknown }
  | { oc: 1; t: "reply"; id: number; ok: false; error: string }
  | { oc: 1; t: "event"; name: string; data: unknown };
