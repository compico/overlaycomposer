import type { ObsInfo } from "../../../../shared/types.ts";
import { obsBadge, sceneText } from "./model.ts";

/** Бейджи «OBS: …» и «scene: …» в шапке. */
export function mountObsStatus(refs: { badge: HTMLElement; scene: HTMLElement }) {
  return {
    /** Полный статус (с обнулением сцены, если её нет). */
    show(obs: ObsInfo) {
      const b = obsBadge(obs);
      refs.badge.textContent = b.text;
      refs.badge.className = `badge ${b.ok ? "ok" : "bad"}`;
      refs.badge.title = b.title;
      refs.scene.textContent = sceneText(obs.scene);
    },
    /** Только сцена (событие смены сцены). */
    setScene(name: string) { refs.scene.textContent = sceneText(name); },
  };
}
