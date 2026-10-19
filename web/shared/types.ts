// Общая модель данных. Source и LayoutObject — разные сущности:
// Source описывает "что это", LayoutObject — "как и где оно показано в layout".

export type SourceType = "iframe" | "image" | "video" | "text" | "html";

export interface Source {
  id: string;
  name: string;
  type: SourceType;
  /**
   * Для iframe/image/video: { url }. Для text/html: зарезервировано (MVP-2).
   * proxy: true — загружать страницу через прокси нашего сервера
   * (для сайтов, запрещающих встраивание через X-Frame-Options / CSP frame-ancestors).
   */
  data: { url?: string; proxy?: boolean; [k: string]: unknown };
}

export interface Transform {
  x: number;
  y: number;
  z: number;
  width: number;
  height: number;
  rotationX: number;
  rotationY: number;
  /** градусы, по часовой стрелке (2D-режим) */
  rotationZ: number;
  scaleX: number;
  scaleY: number;
  scaleZ: number;
}

export interface LayoutObject {
  id: string;
  /** Имя слоя в списке Layers; пусто — берётся имя source. Переименование source слои не затрагивает. */
  name?: string;
  sourceId: string;
  /**
   * Общий идентификатор «экземпляра» виджета. Объекты с одним instanceId в разных layout — один и тот же виджет
   * в разных местах: при смене сцены его iframe не перезагружается. Нет поля — сопоставление по порядку внутри source.
   */
  instanceId?: string;
  /** Координаты в пикселях canvas layout. x/y — левый верхний угол до вращения. */
  transform: Transform;
  opacity: number;
  visible: boolean;
  locked: boolean;
  zIndex: number;
}

export interface Layout {
  id: string;
  name: string;
  /**
   * Имена OBS-сцен, к которым привязан layout. Сцена принадлежит не более чем одному layout.
   * Сцены без своего layout показывают layout по умолчанию (Project.defaultLayoutId).
   */
  scenes: string[];
  canvas: { width: number; height: number };
  objects: LayoutObject[];
}

export interface Project {
  sources: Source[];
  /** layout, который показывается для сцен без собственного layout */
  defaultLayoutId?: string | null;
}

/**
 * Что известно об OBS. Основной источник — сам overlay, запущенный в OBS Browser Source
 * (window.obsstudio); запасной — obs-websocket (опционально).
 */
export interface ObsInfo {
  /** Есть ли живой источник данных о сцене */
  connected: boolean;
  via: "overlay" | "websocket" | null;
  /** Page permissions у overlay внутри OBS (0–5); null — overlay в OBS не запущен/неизвестно */
  permissionLevel: number | null;
  scene: string | null;
  scenes: string[];
  /** Что мешает получать данные (для подсказки в UI); null — всё хорошо */
  error: string | null;
}

/** Минимальный уровень Page permissions, при котором работают getCurrentScene/getScenes */
export const OBS_MIN_PERMISSION = 2;

/** Сообщения WebSocket (/ws) */
export type ServerMessage =
  | { type: "hello"; obs: ObsInfo; layoutId: string | null }
  | { type: "obs:status"; obs: ObsInfo }
  | { type: "scene:changed"; scene: string; layoutId: string | null }
  | { type: "layout:updated"; layoutId: string }
  | { type: "layouts:changed" }
  | { type: "sources:changed" };

export type ClientMessage =
  | { type: "layout:activate"; layoutId: string }
  | { type: "source:update"; objectId: string; layoutId?: string; data: Partial<Pick<LayoutObject, "visible" | "opacity">> }
  /** overlay сообщает, что запущен внутри OBS (есть window.obsstudio) и какие у него права */
  | { type: "overlay:hello"; inObs: boolean; permissionLevel: number | null }
  /** overlay из OBS сообщает текущую сцену и/или список сцен */
  | { type: "obs:report"; scene?: string; scenes?: string[] };

export function defaultTransform(): Transform {
  return {
    x: 100, y: 100, z: 0,
    width: 400, height: 300,
    rotationX: 0, rotationY: 0, rotationZ: 0,
    scaleX: 1, scaleY: 1, scaleZ: 1,
  };
}

export function uid(prefix = "id"): string {
  // crypto.randomUUID недоступен в небезопасных контекстах (http по LAN) — запасной вариант.
  const rnd = typeof crypto !== "undefined" && "randomUUID" in crypto
    ? crypto.randomUUID().slice(0, 8)
    : Math.random().toString(36).slice(2, 10);
  return `${prefix}_${rnd}`;
}
