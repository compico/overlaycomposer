// Общая модель данных. Source и LayoutObject — разные сущности:
// Source описывает "что это", LayoutObject — "как и где оно показано в layout".

export type SourceType = "iframe" | "plugin" | "image" | "video" | "text" | "html";

export interface Source {
  id: string;
  name: string;
  type: SourceType;
  /**
   * Для iframe/image/video: { url }. Для text/html: зарезервировано (MVP-2).
   * proxy: true — загружать страницу через прокси нашего сервера
   * (для сайтов, запрещающих встраивание через X-Frame-Options / CSP frame-ancestors).
   */
  data: {
    url?: string;
    proxy?: boolean;
    /** type "plugin": id плагина-виджета и его настройки (передаются виджету в query-параметрах) */
    plugin?: string;
    settings?: Record<string, string | number | boolean>;
    [k: string]: unknown;
  };
}

/** Настройка плагина-виджета (схема из plugin.json). */
export interface PluginSetting {
  key: string;
  label: string;
  type: "string" | "text" | "number" | "range" | "bool" | "color" | "font" | "select";
  group?: string;
  help?: string;
  default?: string | number | boolean;
  options?: { value: string; label: string }[];
  min?: number;
  max?: number;
  step?: number;
}

export interface PluginManifest {
  id: string;
  name: string;
  version?: string;
  description?: string;
  author?: string;
  entry?: string;
  defaultSize: { width: number; height: number };
  settings?: PluginSetting[];
  builtin: boolean;
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
  /**
   * Обрезка как в OBS: сколько пикселей виджета (до масштаба) скрыто с каждой стороны; не задано = 0.
   * x/y/width/height описывают весь виджет, а видимая часть — прямоугольник внутри него (см. visibleBox).
   */
  cropLeft?: number;
  cropTop?: number;
  cropRight?: number;
  cropBottom?: number;
}

/**
 * Видимая часть слоя. cx/cy — её центр на canvas (вокруг него работают поворот и масштаб), w/h — размер до масштаба,
 * ox/oy — смещение центра видимой части от центра всего виджета в его локальных пикселях.
 * Без обрезки совпадает с прежним поведением (центр виджета).
 */
export function visibleBox(t: Transform) {
  const cl = Math.max(0, t.cropLeft ?? 0), cr = Math.max(0, t.cropRight ?? 0);
  const ct = Math.max(0, t.cropTop ?? 0), cb = Math.max(0, t.cropBottom ?? 0);
  const w = Math.max(1, t.width - cl - cr), h = Math.max(1, t.height - ct - cb);
  const ox = (cl - cr) / 2, oy = (ct - cb) / 2;
  return { cl, cr, ct, cb, w, h, ox, oy, cx: t.x + t.width / 2 + ox, cy: t.y + t.height / 2 + oy };
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

/** Физический центр всего виджета (вместе с обрезанной частью) на canvas — он не должен двигаться при обрезке. */
export function contentCenter(t: Transform): { x: number; y: number } {
  const v = visibleBox(t);
  const o = localToCanvas(t, v.ox, v.oy);
  return { x: v.cx - o.x, y: v.cy - o.y };
}

/** Подбирает x/y так, чтобы физический центр виджета остался в точке f (после изменения обрезки). */
export function placeByContentCenter(t: Transform, f: { x: number; y: number }) {
  const v = visibleBox(t);
  const o = localToCanvas(t, v.ox, v.oy);
  t.x = Math.round(f.x + o.x - v.ox - t.width / 2);
  t.y = Math.round(f.y + o.y - v.oy - t.height / 2);
}

/** Вектор из локальных пикселей виджета в пиксели canvas: масштаб, затем поворот по часовой. */
export function localToCanvas(t: Transform, lx: number, ly: number) {
  const th = (t.rotationZ * Math.PI) / 180;
  const cos = Math.cos(th), sin = Math.sin(th);
  const sx = lx * (t.scaleX || 1), sy = ly * (t.scaleY || 1);
  return { x: sx * cos - sy * sin, y: sx * sin + sy * cos };
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
