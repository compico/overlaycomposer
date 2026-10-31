import * as THREE from "three";
import { CSS3DObject, CSS3DRenderer } from "three/addons/renderers/CSS3DRenderer.js";
import type { Layout, LayoutObject, Source } from "../../shared/types.ts";
import { visibleBox } from "../../shared/types.ts";
import type { PluginHost } from "./pluginhost.ts";
import { proxyBase } from "./proxybase.ts";

const FOV = 50;

/**
 * Значение атрибута allow для iframe: только то, что браузер понимает. Firefox, например, не знает
 * «autoplay» и пишет в консоль «Feature Policy: Skipping unsupported feature name» (на работу это не влияет,
 * но шумит). Автовоспроизведение в Firefox разрешается в настройках сайта, а не этим атрибутом;
 * в OBS (CEF) оно разрешено по умолчанию.
 */
function frameAllow(): string {
  const want = ["autoplay", "fullscreen"];
  const fp = (document as unknown as { featurePolicy?: { allowedFeatures?: () => string[] } }).featurePolicy;
  const known = fp?.allowedFeatures?.();
  return (known ? want.filter((f) => known.includes(f)) : want).join("; ");
}
const FRAME_ALLOW = frameAllow();

/**
 * Адрес для iframe. Для источников с proxy:true страница грузится через наш сервер
 * (/proxy/<id>/<путь на сайте>), который снимает запрет на встраивание.
 */
function frameUrl(src: Source): string | null {
  if (src.type === "plugin") {
    // Плагин-виджет: его файлы раздаёт сервер, а настройки уходят виджету в query (?channel=…&msgSize=…).
    if (!src.data.plugin) return null;
    const q = new URLSearchParams();
    for (const [k, v] of Object.entries(src.data.settings ?? {})) q.set(k, String(v));
    const qs = q.toString();
    return `/plugins/${encodeURIComponent(src.data.plugin)}/${qs ? `?${qs}` : ""}`;
  }
  const url = src.data.url;
  if (!url) return null;
  if (src.data.proxy) {
    try {
      const u = new URL(url);
      if (u.protocol === "http:" || u.protocol === "https:") return `${proxyBase()}/proxy/${src.id}${u.pathname}${u.search}${u.hash}`;
    } catch { /* некорректный URL — отдаём как есть */ }
  }
  return url;
}

interface Entry {
  key: string;
  obj3d: CSS3DObject;
  el: HTMLDivElement;
  frame: HTMLIFrameElement;
  url: string;
  /** Выданные плагину разрешения (меняются без смены url — тогда запись пересоздаётся). */
  granted: string;
}

/**
 * Объект «плоский»: без поворота вокруг X/Y и без сдвига по глубине. Если плоские все видимые объекты layout, perspective и
 * 3D-контекст страницы не нужны: результат тот же, а браузеру (особенно CEF в OBS при нагрузке на GPU) нечего сортировать
 * и композировать в 3D.
 */
function isFlat(o: LayoutObject): boolean {
  const t = o.transform;
  return t.rotationX === 0 && t.rotationY === 0 && t.z === 0 && t.scaleZ === 1;
}

/**
 * Рендер layout через Three.js + CSS3DRenderer.
 * Сцена в пикселях canvas layout: камера подобрана так, что плоскость z=0
 * отображается 1:1. Это даёт «задел под 3D» (z, rotationX/Y), хотя MVP — 2D.
 *
 * Перерисовка только по изменению (iframe анимируются браузером сами,
 * rAF-цикл не нужен — экономит CPU внутри OBS).
 */
export class OverlayScene {
  readonly root: HTMLDivElement;
  #scene = new THREE.Scene();
  #camera = new THREE.PerspectiveCamera(FOV, 1, 1, 100000);
  #renderer = new CSS3DRenderer();
  #pool = new Map<string, Entry>();
  #sources = new Map<string, Source>();
  #layout: Layout | null = null;
  #scale = 1;
  #flat = false;
  /** Принудительный 3D-режим для сравнения (в overlay: ?render=3d). */
  #force3d = false;

  constructor(private host: HTMLElement, private opts: { interactive: boolean; plugins?: PluginHost; render?: "auto" | "3d" }) {
    this.#force3d = opts.render === "3d";
    this.root = document.createElement("div");
    this.root.style.cssText = "position:absolute;left:0;top:0;transform-origin:0 0;";
    this.#renderer.domElement.style.cssText = "position:absolute;left:0;top:0;";
    this.root.appendChild(this.#renderer.domElement);
    host.appendChild(this.root);
  }

  get scale() { return this.#scale; }

  setSources(sources: Source[]) {
    this.#sources = new Map(sources.map((s) => [s.id, s]));
    if (this.#layout) this.setLayout(this.#layout);
  }

  /** Масштаб «вписать в host» (для runtime) или явный (для editor). */
  fit(mode: "contain" | { scale: number; offsetX: number; offsetY: number }) {
    const l = this.#layout;
    if (!l) return;
    if (mode === "contain") {
      const k = Math.min(this.host.clientWidth / l.canvas.width, this.host.clientHeight / l.canvas.height);
      this.#scale = k;
      const ox = (this.host.clientWidth - l.canvas.width * k) / 2;
      const oy = (this.host.clientHeight - l.canvas.height * k) / 2;
      this.root.style.transform = `translate(${ox}px,${oy}px) scale(${k})`;
    } else {
      this.#scale = mode.scale;
      this.root.style.transform = `translate(${mode.offsetX}px,${mode.offsetY}px) scale(${mode.scale})`;
    }
  }

  setLayout(layout: Layout) {
    const sizeChanged = !this.#layout ||
      this.#layout.canvas.width !== layout.canvas.width ||
      this.#layout.canvas.height !== layout.canvas.height;
    this.#layout = layout;

    if (sizeChanged) this.#applyCanvas();
    const flat = !this.#force3d && layout.objects.every((o) => !o.visible || isFlat(o));
    if (this.#flat && !flat) this.#leaveFlat();
    this.#flat = flat;

    // Ключ пула: instanceId (явная связь, в т.ч. копии между layout) либо sourceId#n (n — порядковый номер среди объектов
    // этого source без instanceId). Один и тот же ключ в соседних layout переиспользует тот же iframe
    // (виджет не перезагружается при смене сцены). Если ключ в layout уже занят — добавляем суффикс.
    const counters = new Map<string, number>();
    const used = new Set<string>();

    for (const o of layout.objects) {
      const src = this.#sources.get(o.sourceId);
      const url = src && (src.type === "iframe" || src.type === "plugin") ? frameUrl(src) : null;
      if (!src || !url) continue;
      let key: string;
      if (o.instanceId) {
        key = `i:${o.instanceId}`;
      } else {
        const n = counters.get(o.sourceId) ?? 0;
        counters.set(o.sourceId, n + 1);
        key = `${o.sourceId}#${n}`;
      }
      for (let j = 1, base = key; used.has(key); j++) key = `${base}~${j}`;
      used.add(key);

      let e = this.#pool.get(key);
      const granted = src.type === "plugin" ? JSON.stringify(src.data.granted ?? []) : "";
      if (e && (e.url !== url || e.granted !== granted)) {
        this.#removeEntry(e);
        e = undefined;
      }
      if (!e) {
        e = this.#createEntry(key, url, src.type === "plugin", granted);
        if (src.type === "plugin") {
          this.opts.plugins?.register(e.frame, {
            pluginId: src.data.plugin ?? "", sourceId: src.id, settings: src.data.settings ?? {}, permissions: src.data.granted ?? [],
          });
        }
        this.#pool.set(key, e);
        this.#scene.add(e.obj3d);
      }
      this.#applyObject(e, o, layout);
    }

    for (const [key, e] of this.#pool) {
      if (!used.has(key)) {
        this.#removeEntry(e);
        this.#pool.delete(key);
      }
    }
    this.render();
  }

  render() {
    this.#renderer.render(this.#scene, this.#camera);
    if (this.#flat) this.#flattenCamera();
  }

  /** Элемент, в который CSS3DRenderer кладёт объекты (domElement → view → camera). */
  #cameraElement(): HTMLElement {
    return this.#renderer.domElement.firstElementChild!.firstElementChild as HTMLElement;
  }

  /**
   * Плоский режим: заменяем perspective-камеру на сдвиг в центр холста. Для объектов с z=0 и без наклонов матрица
   * matrix3d у каждого объекта — обычное 2D-преобразование, и картинка пиксель в пиксель такая же, как с перспективой.
   */
  #flattenCamera() {
    const c = this.#cameraElement();
    const { width: W, height: H } = this.#layout!.canvas;
    c.style.transformStyle = "flat";
    // Начало преобразования камеры — её центр (transform-origin по умолчанию), мир CSS3DRenderer — «y вверх»: сдвиг по x и
    // зеркало по y (с компенсацией сдвига) дают те же пиксели, что матрица камеры в 3D-режиме.
    c.style.transform = `translate(${W / 2}px,${-H / 2}px) scale(1,-1)`;
  }

  /**
   * Возврат в 3D: CSS3DRenderer помнит прежний стиль камеры и не перезапишет его сам. Сдвигаем камеру на 1, рисуем (стиль
   * отличается — записывается), возвращаем и рисуем ещё раз.
   */
  #leaveFlat() {
    this.#cameraElement().style.transformStyle = "preserve-3d";
    this.#camera.position.z += 1;
    this.#renderer.render(this.#scene, this.#camera);
    this.#camera.position.z -= 1;
    this.#renderer.render(this.#scene, this.#camera);
  }

  destroy() {
    for (const e of this.#pool.values()) this.#removeEntry(e);
    this.#pool.clear();
    this.root.remove();
  }

  #applyCanvas() {
    const { width: W, height: H } = this.#layout!.canvas;
    this.#renderer.setSize(W, H);
    this.#camera.aspect = W / H;
    this.#camera.position.set(0, 0, (H / 2) / Math.tan(THREE.MathUtils.degToRad(FOV / 2)));
    this.#camera.updateProjectionMatrix();
    this.root.style.width = `${W}px`;
    this.root.style.height = `${H}px`;
  }

  #createEntry(key: string, url: string, sandboxed: boolean, granted: string): Entry {
    const el = document.createElement("div");
    el.style.overflow = "hidden";
    const frame = document.createElement("iframe");
    // Плагины — чужой код: iframe без allow-same-origin получает «непрозрачный» origin и не может обращаться к API приложения.
    if (sandboxed) frame.setAttribute("sandbox", "allow-scripts allow-forms allow-popups allow-modals");
    frame.src = url;
    frame.loading = "eager";
    if (FRAME_ALLOW) frame.allow = FRAME_ALLOW;
    frame.style.cssText = "position:absolute;left:0;top:0;width:100%;height:100%;border:0;background:transparent;display:block;";
    frame.setAttribute("allowtransparency", "true");
    // В редакторе iframe не должен перехватывать мышь — иначе нельзя выделять/двигать.
    if (this.opts.interactive) frame.style.pointerEvents = "none";
    el.appendChild(frame);
    const obj3d = new CSS3DObject(el);
    return { key, obj3d, el, frame, url, granted };
  }

  #removeEntry(e: Entry) {
    this.opts.plugins?.unregister(e.frame);
    this.#scene.remove(e.obj3d);
    e.frame.src = "about:blank";
    e.el.remove();
  }

  #applyObject(e: Entry, o: LayoutObject, layout: Layout) {
    const t = o.transform;
    const W = layout.canvas.width;
    const H = layout.canvas.height;
    // Обрезка: контейнер — видимая часть (overflow:hidden), iframe полного размера сдвинут внутри него.
    const v = visibleBox(t);
    e.el.style.width = `${v.w}px`;
    e.el.style.height = `${v.h}px`;
    e.frame.style.left = `${-v.cl}px`;
    e.frame.style.top = `${-v.ct}px`;
    e.frame.style.width = `${t.width}px`;
    e.frame.style.height = `${t.height}px`;
    e.el.style.opacity = String(o.opacity);

    // Пиксели canvas (y вниз, начало слева сверху) -> мировые координаты (y вверх, центр).
    // Небольшой z-сдвиг по zIndex упорядочивает копланарные элементы.
    e.obj3d.position.set(
      v.cx - W / 2,
      -(v.cy - H / 2),
      this.#flat ? 0 : t.z + o.zIndex * 0.01,
    );
    e.el.style.zIndex = String(o.zIndex); // в плоском режиме порядок слоёв задаёт z-index, а не глубина
    e.obj3d.rotation.set(
      THREE.MathUtils.degToRad(t.rotationX),
      THREE.MathUtils.degToRad(t.rotationY),
      -THREE.MathUtils.degToRad(t.rotationZ), // по часовой стрелке
    );
    e.obj3d.scale.set(t.scaleX, t.scaleY, t.scaleZ);
    e.obj3d.visible = o.visible;
    // Скрытые iframe не рисуются (display:none) — браузер экономит ресурсы.
  }
}
