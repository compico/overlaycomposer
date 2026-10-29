// Статус связи с OBS в шапке: что написать на бейдже. Чистые функции — тестируются в Node.
import type { ObsInfo } from "../../../../shared/types.ts";

export interface ObsBadge {
  text: string;
  ok: boolean;
  title: string;
}

export function obsBadge(obs: Pick<ObsInfo, "connected" | "via" | "error">): ObsBadge {
  if (!obs.connected) {
    return { text: "OBS: no data ⓘ", ok: false, title: `${obs.error ?? "Not connected"}\n\nClick for details` };
  }
  const overlay = obs.via === "overlay";
  return {
    text: `OBS: connected (${overlay ? "overlay" : "websocket"})`,
    ok: true,
    title: `Scene data comes from ${overlay ? "the overlay running in OBS" : "obs-websocket"}`,
  };
}

export const sceneText = (scene: string | null | undefined): string => (scene ? `scene: ${scene}` : "");
