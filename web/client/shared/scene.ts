import * as THREE from "three";
import { CSS3DObject, CSS3DRenderer } from "three/addons/renderers/CSS3DRenderer.js";
import type { Layout, LayoutObject, Source } from "../../shared/types.ts";
import { visibleBox } from "../../shared/types.ts";

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
  const url = src.data.url;
  if (!url) return null;
  if (src.data.proxy) {
    try {
      const u = new URL(url);
      if (u.protocol === "http:" || u.protocol === "https:") return `/proxy/${src.id}${u.pathname}${u.search}${u.hash}`;
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

  constructor(private host: HTMLElement, private opts: { interactive: boolean }) {
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

    // Ключ пула: instanceId (явная связь, в т.ч. копии между layout) либо sourceId#n (n — порядковый номер среди объектов
    // этого source без instanceId). Один и тот же ключ в соседних layout переиспользует тот же iframe
    // (виджет не перезагружается при смене сцены). Если ключ в layout уже занят — добавляем суффикс.
    const counters = new Map<string, number>();
    const used = new Set<string>();

    for (const o of layout.objects) {
      const src = this.#sources.get(o.sourceId);
      const url = src && src.type === "iframe" ? frameUrl(src) : null;
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
      if (e && e.url !== url) {
        this.#removeEntry(e);
        e = undefined;
      }
      if (!e) {
        e = this.#createEntry(key, url);
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

  #createEntry(key: string, url: string): Entry {
    const el = document.createElement("div");
    el.style.overflow = "hidden";
    const frame = document.createElement("iframe");
    frame.src = url;
    frame.loading = "eager";
    if (FRAME_ALLOW) frame.allow = FRAME_ALLOW;
    frame.style.cssText = "position:absolute;left:0;top:0;width:100%;height:100%;border:0;background:transparent;display:block;";
    frame.setAttribute("allowtransparency", "true");
    // В редакторе iframe не должен перехватывать мышь — иначе нельзя выделять/двигать.
    if (this.opts.interactive) frame.style.pointerEvents = "none";
    el.appendChild(frame);
    const obj3d = new CSS3DObject(el);
    return { key, obj3d, el, frame, url };
  }

  #removeEntry(e: Entry) {
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
      t.z + o.zIndex * 0.01,
    );
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
