// Панель свойств: поля трансформации и обрезки (чистая логика), операции над источником и layout. Без DOM.
import type { LayoutObject, Source, Transform } from "../../../../shared/types.ts";
import { contentCenter, placeByContentCenter } from "../../../../shared/types.ts";
import type { Api } from "../../lib/api.ts";

/** Минимальный размер видимой части виджета, px. */
export const MIN_SIZE = 20;

export type CropSide = "cropLeft" | "cropTop" | "cropRight" | "cropBottom";

/** Задать одну сторону обрезки с ограничениями (≥ 0, остаётся видимая часть) и сохранением положения содержимого. */
export function setCrop(t: Transform, side: CropSide, value: number, keep = contentCenter(t)) {
  const other = side === "cropLeft" ? t.cropRight : side === "cropRight" ? t.cropLeft : side === "cropTop" ? t.cropBottom : t.cropTop;
  const total = side === "cropLeft" || side === "cropRight" ? t.width : t.height;
  const max = Math.max(0, total - (other ?? 0) - MIN_SIZE);
  t[side] = Math.round(Math.min(max, Math.max(0, value)));
  placeByContentCenter(t, keep);
}

/** Сбросить обрезку так, чтобы содержимое осталось на месте. */
export function resetCrop(t: Transform) {
  const f = contentCenter(t);
  t.cropLeft = t.cropRight = t.cropTop = t.cropBottom = 0;
  placeByContentCenter(t, f);
}

export interface NumSpec {
  label: string;
  get(): number;
  set(v: number): void;
  step: number;
}

/** Числовые поля блока Transform для слоя. */
export function transformFields(o: LayoutObject): NumSpec[] {
  const t = o.transform;
  return [
    { label: "X", get: () => t.x, set: (v) => (t.x = v), step: 1 },
    { label: "Y", get: () => t.y, set: (v) => (t.y = v), step: 1 },
    { label: "Width", get: () => t.width, set: (v) => (t.width = Math.max(1, v, (t.cropLeft ?? 0) + (t.cropRight ?? 0) + 1)), step: 1 },
    { label: "Height", get: () => t.height, set: (v) => (t.height = Math.max(1, v, (t.cropTop ?? 0) + (t.cropBottom ?? 0) + 1)), step: 1 },
    { label: "Rotation °", get: () => t.rotationZ, set: (v) => (t.rotationZ = v), step: 0.5 },
    { label: "Opacity %", get: () => o.opacity * 100, set: (v) => (o.opacity = Math.min(1, Math.max(0, v / 100))), step: 5 },
    { label: "Scale X", get: () => t.scaleX, set: (v) => (t.scaleX = v), step: 0.05 },
    { label: "Scale Y", get: () => t.scaleY, set: (v) => (t.scaleY = v), step: 0.05 },
  ];
}

/** Размер холста из полей ввода: пустое/нулевое → умолчание, минимум 16. */
export function canvasFromInputs(w: string, h: string): { width: number; height: number } {
  return { width: Math.max(16, Number(w) || 1920), height: Math.max(16, Number(h) || 1080) };
}

export interface PropertiesModel {
  renameSource(src: Source, name: string): Promise<void>;
  setSourceUrl(src: Source, url: string): Promise<void>;
  setSourceProxy(src: Source, proxy: boolean): Promise<void>;
  deleteSource(src: Source): Promise<void>;
  setDefaultLayout(layoutId: string | null): Promise<void>;
}

export function createPropertiesModel(api: Api): PropertiesModel {
  const put = (src: Source, body: unknown) => api(`/api/sources/${src.id}`, "PUT", body);
  return {
    async renameSource(src, name) { await put(src, { name }); },
    async setSourceUrl(src, url) { await put(src, { data: { url } }); },
    async setSourceProxy(src, proxy) { await put(src, { data: { proxy } }); },
    async deleteSource(src) { await api(`/api/sources/${src.id}`, "DELETE"); },
    async setDefaultLayout(layoutId) { await api("/api/default-layout", "PUT", { layoutId }); },
  };
}
