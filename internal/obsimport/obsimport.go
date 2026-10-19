// Package obsimport — импорт сцены OBS как layout: Browser Source → source + объект layout с тем же размером и положением.
//
// Читает сцену через obs-websocket (GetSceneItemList / GetGroupSceneItemList / GetInputSettings) и обходит группы
// и вложенные сцены, складывая их преобразования (позиция, масштаб, поворот, выравнивание) в одно итоговое.
// Группы при импорте «разворачиваются» в плоский список: выглядит так же, а имя папки попадает в имя слоя.
package obsimport

import (
	"fmt"
	"math"
	"net/url"
	"sort"
	"strings"

	"github.com/compico/overlaycomposer/internal/model"
)

// Requester — запросы к OBS (реализует obs.Client).
type Requester interface {
	Request(typ string, data map[string]any) (map[string]any, bool)
}

type Skipped struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// Result — что получилось. Sources — только новые источники (существующие с тем же URL переиспользуются).
type Result struct {
	Layout   model.Layout   `json:"layout"`
	Sources  []model.Source `json:"newSources"`
	Reused   int            `json:"reusedSources"`
	Proxied  int            `json:"proxied"`
	Skipped  []Skipped      `json:"skipped"`
	Warnings []string       `json:"warnings"`
}

const maxDepth = 6

// ---- аффинное преобразование 2×3: (x, y) → (a·x + c·y + e, b·x + d·y + f) ----

type affine struct{ a, b, c, d, e, f float64 }

var identity = affine{a: 1, d: 1}

func (m affine) apply(x, y float64) (float64, float64) {
	return m.a*x + m.c*y + m.e, m.b*x + m.d*y + m.f
}

// mul возвращает m·n: сначала n, затем m.
func (m affine) mul(n affine) affine {
	return affine{
		a: m.a*n.a + m.c*n.b, b: m.b*n.a + m.d*n.b,
		c: m.a*n.c + m.c*n.d, d: m.b*n.c + m.d*n.d,
		e: m.a*n.e + m.c*n.f + m.e, f: m.b*n.e + m.d*n.f + m.f,
	}
}

func num(m map[string]any, key string) float64 {
	switch v := m[key].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	}
	return 0
}

func str(m map[string]any, key string) string { s, _ := m[key].(string); return s }
func boolean(m map[string]any, key string, def bool) bool {
	if b, ok := m[key].(bool); ok {
		return b
	}
	return def
}

// itemMatrix — из пространства источника (0..sw, 0..sh) в пространство родителя.
// OBS: p = pos + R(rot)·(S·u − align·size), где size — итоговый (после масштаба/bounds) размер элемента.
func itemMatrix(t map[string]any, sw, sh float64) affine {
	w, h := num(t, "width"), num(t, "height")
	if w <= 0 {
		w = sw * num(t, "scaleX")
	}
	if h <= 0 {
		h = sh * num(t, "scaleY")
	}
	sx, sy := 1.0, 1.0
	if sw > 0 {
		sx = w / sw
	}
	if sh > 0 {
		sy = h / sh
	}
	align := int(num(t, "alignment"))
	ax, ay := 0.5, 0.5 // центр по умолчанию, если бит не задан
	switch {
	case align&1 != 0:
		ax = 0
	case align&2 != 0:
		ax = 1
	}
	switch {
	case align&4 != 0:
		ay = 0
	case align&8 != 0:
		ay = 1
	}
	rad := num(t, "rotation") * math.Pi / 180
	cos, sin := math.Cos(rad), math.Sin(rad)
	// R·(S·u − off) + pos
	ox, oy := ax*w, ay*h
	return affine{
		a: cos * sx, b: sin * sx,
		c: -sin * sy, d: cos * sy,
		e: num(t, "positionX") - cos*ox + sin*oy,
		f: num(t, "positionY") - sin*ox - cos*oy,
	}
}

// decompose извлекает из итоговой матрицы прямоугольника sw×sh: центр, масштаб и поворот (по часовой, градусы).
func decompose(m affine, sw, sh float64) (cx, cy, scaleX, scaleY, rot float64, skewed bool) {
	cx, cy = m.apply(sw/2, sh/2)
	scaleX = math.Hypot(m.a, m.b)
	scaleY = math.Hypot(m.c, m.d)
	rot = math.Atan2(m.b, m.a) * 180 / math.Pi
	// столбцы должны быть ортогональны, иначе есть скос, который в нашей модели не выразить
	skewed = scaleX > 0 && scaleY > 0 && math.Abs(m.a*m.c+m.b*m.d)/(scaleX*scaleY) > 0.01
	return
}

// ---- хост/URL ----

// IsLocalHost — адреса, для которых прокси не нужен.
func IsLocalHost(host string) bool {
	h := strings.ToLower(strings.Trim(host, "[]"))
	return h == "localhost" || h == "127.0.0.1" || h == "0.0.0.0" || h == "::1" || h == "::" || strings.HasSuffix(h, ".localhost")
}

type importer struct {
	r        Requester
	existing []model.Source
	res      *Result
	srcByKey map[string]string // url|proxy → id источника
	usedIDs  map[string]bool
	inputs   map[string]string // имя входа OBS → id источника ("" — пропущен)
	z        int
}

// Import читает сцену scene и собирает layout (id/привязки назначает вызывающий).
func Import(r Requester, scene string, existing []model.Source) (*Result, error) {
	res := &Result{Layout: model.Layout{Name: scene, Scenes: []string{}, Canvas: model.Canvas{Width: 1920, Height: 1080}, Objects: []model.Object{}}}
	im := &importer{r: r, existing: existing, res: res, srcByKey: map[string]string{}, usedIDs: map[string]bool{}, inputs: map[string]string{}}
	for _, s := range existing {
		im.usedIDs[s.ID] = true
		im.srcByKey[srcKey(s.Data.URL, s.Data.Proxy)] = s.ID
	}
	if v, ok := r.Request("GetVideoSettings", nil); ok {
		if w, h := int(num(v, "baseWidth")), int(num(v, "baseHeight")); w > 0 && h > 0 {
			res.Layout.Canvas = model.Canvas{Width: w, Height: h}
		}
	}
	if err := im.walk(scene, false, identity, true, "", 0); err != nil {
		return nil, err
	}
	return res, nil
}

func srcKey(u string, proxy bool) string { return fmt.Sprintf("%s|%t", u, proxy) }

func (im *importer) warn(format string, a ...any) {
	im.res.Warnings = append(im.res.Warnings, fmt.Sprintf(format, a...))
}
func (im *importer) skip(name, reason string) {
	im.res.Skipped = append(im.res.Skipped, Skipped{Name: name, Reason: reason})
}

// walk обходит элементы сцены или группы. parent — преобразование пространства контейнера в canvas корня.
func (im *importer) walk(container string, isGroup bool, parent affine, visible bool, path string, depth int) error {
	req, key := "GetSceneItemList", "sceneName"
	if isGroup {
		req = "GetGroupSceneItemList"
	}
	resp, ok := im.r.Request(req, map[string]any{key: container})
	if !ok {
		if depth == 0 {
			return fmt.Errorf("OBS не вернул элементы сцены «%s» (сцена удалена или нет связи)", container)
		}
		im.warn("не удалось прочитать «%s»", container)
		return nil
	}
	raw, _ := resp["sceneItems"].([]any)
	items := make([]map[string]any, 0, len(raw))
	for _, x := range raw {
		if m, ok := x.(map[string]any); ok {
			items = append(items, m)
		}
	}
	sort.SliceStable(items, func(i, j int) bool { return num(items[i], "sceneItemIndex") < num(items[j], "sceneItemIndex") }) // снизу вверх

	for _, it := range items {
		name := str(it, "sourceName")
		label := name
		if path != "" {
			label = path + " / " + name
		}
		t, _ := it["sceneItemTransform"].(map[string]any)
		if t == nil {
			t = map[string]any{}
		}
		vis := visible && boolean(it, "sceneItemEnabled", true)
		sw, sh := num(t, "sourceWidth"), num(t, "sourceHeight")

		switch {
		case boolean(it, "isGroup", false):
			if depth >= maxDepth {
				im.skip(label, "слишком глубокая вложенность")
				continue
			}
			if err := im.walk(name, true, parent.mul(itemMatrix(t, sw, sh)), vis, label, depth+1); err != nil {
				return err
			}
		case str(it, "sourceType") == "OBS_SOURCE_TYPE_SCENE":
			if depth >= maxDepth {
				im.skip(label, "слишком глубокая вложенность")
				continue
			}
			if err := im.walk(name, false, parent.mul(itemMatrix(t, sw, sh)), vis, label, depth+1); err != nil {
				return err
			}
		case str(it, "inputKind") == "browser_source":
			im.browser(it, t, name, label, parent, vis, sw, sh)
		default:
			kind := str(it, "inputKind")
			if kind == "" {
				kind = str(it, "sourceType")
			}
			im.skip(label, "не Browser Source ("+kind+")")
		}
	}
	return nil
}

func (im *importer) browser(it, t map[string]any, name, label string, parent affine, visible bool, sw, sh float64) {
	srcID, known := im.inputs[name]
	var settings map[string]any
	if !known {
		resp, ok := im.r.Request("GetInputSettings", map[string]any{"inputName": name})
		if !ok {
			im.skip(label, "не удалось прочитать настройки источника")
			return
		}
		settings, _ = resp["inputSettings"].(map[string]any)
		var reason string
		srcID, reason = im.sourceFor(name, settings)
		im.inputs[name] = srcID
		if srcID == "" {
			im.skip(label, reason)
			return
		}
	} else if srcID == "" {
		im.skip(label, "см. выше (тот же источник)")
		return
	}
	if sw <= 0 || sh <= 0 {
		// запасной вариант: размер из настроек Browser Source
		if settings != nil {
			sw, sh = num(settings, "width"), num(settings, "height")
		}
		if sw <= 0 || sh <= 0 {
			sw, sh = 800, 600
			im.warn("«%s»: размер не получен, взят 800×600", label)
		}
		if _, hasW := t["width"]; !hasW {
			t = map[string]any{"width": sw, "height": sh, "positionX": num(t, "positionX"), "positionY": num(t, "positionY")}
		}
	}
	m := parent.mul(itemMatrix(t, sw, sh))
	cx, cy, scx, scy, rot, skewed := decompose(m, sw, sh)
	if skewed {
		im.warn("«%s»: в группе есть скос/неравномерный поворот — положение приблизительное", label)
	}
	if num(t, "scaleX") < 0 || num(t, "scaleY") < 0 {
		im.warn("«%s»: отражение (flip) не поддерживается и проигнорировано", label)
	}
	if num(t, "cropLeft")+num(t, "cropRight")+num(t, "cropTop")+num(t, "cropBottom") > 0 {
		im.warn("«%s»: обрезка (crop) не поддерживается и проигнорирована", label)
	}
	im.z++
	im.res.Layout.Objects = append(im.res.Layout.Objects, model.Object{
		ID:       fmt.Sprintf("obj_imp%d", im.z),
		Name:     label,
		SourceID: srcID,
		Transform: model.Transform{
			X: round2(cx - sw/2), Y: round2(cy - sh/2),
			Width: sw, Height: sh,
			RotationZ: round2(normDeg(rot)),
			ScaleX:    round4(scx), ScaleY: round4(scy), ScaleZ: 1,
		},
		Opacity: 1,
		Visible: visible,
		Locked:  boolean(it, "sceneItemLocked", false),
		ZIndex:  im.z,
	})
}

// sourceFor находит/создаёт source для Browser Source. Пустой id — пропуск (reason объясняет почему).
func (im *importer) sourceFor(name string, settings map[string]any) (id, reason string) {
	if boolean(settings, "is_local_file", false) {
		return "", "локальный файл (" + str(settings, "local_file") + ") — пока не поддерживается"
	}
	raw := strings.TrimSpace(str(settings, "url"))
	if raw == "" {
		return "", "пустой URL"
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", "URL не http(s): " + raw
	}
	local := IsLocalHost(u.Hostname())
	if local && strings.TrimRight(u.Path, "/") == "/overlay" {
		return "", "это сам Overlay Composer"
	}
	proxy := !local // внешние сайты — через прокси «на всякий случай»
	k := srcKey(raw, proxy)
	if id, ok := im.srcByKey[k]; ok {
		im.res.Reused++
		return id, ""
	}
	id = im.uniqueID(name)
	im.usedIDs[id] = true
	im.srcByKey[k] = id
	im.res.Sources = append(im.res.Sources, model.Source{ID: id, Name: name, Type: "iframe", Data: model.SourceData{URL: raw, Proxy: proxy}})
	if proxy {
		im.res.Proxied++
	}
	return id, ""
}

func (im *importer) uniqueID(name string) string {
	base := model.Slugify(name)
	if base == "layout" { // Slugify отдаёт «layout» для пустого результата
		base = "source"
	}
	id := base
	for i := 2; im.usedIDs[id]; i++ {
		id = fmt.Sprintf("%s-%d", base, i)
	}
	return id
}

func normDeg(d float64) float64 {
	d = math.Mod(d, 360)
	if d < 0 {
		d += 360
	}
	if math.Abs(d-360) < 1e-9 {
		d = 0
	}
	return d
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }
func round4(v float64) float64 { return math.Round(v*10000) / 10000 }
