// Package model — данные, общие для хранилища, API и WebSocket. JSON-формат совпадает с shared/types.ts клиента.
package model

import (
	"math"
	"net/url"
	"regexp"
	"strings"
)

type Transform struct {
	X         float64 `json:"x"`
	Y         float64 `json:"y"`
	Z         float64 `json:"z"`
	Width     float64 `json:"width"`
	Height    float64 `json:"height"`
	RotationX float64 `json:"rotationX"`
	RotationY float64 `json:"rotationY"`
	RotationZ float64 `json:"rotationZ"`
	ScaleX    float64 `json:"scaleX"`
	ScaleY    float64 `json:"scaleY"`
	ScaleZ    float64 `json:"scaleZ"`
	// Обрезка, как crop в OBS: сколько пикселей виджета (до масштаба) скрыто с каждой стороны. Width/Height — полный размер виджета.
	CropLeft   float64 `json:"cropLeft,omitempty"`
	CropTop    float64 `json:"cropTop,omitempty"`
	CropRight  float64 `json:"cropRight,omitempty"`
	CropBottom float64 `json:"cropBottom,omitempty"`
}

// validCrop: обрезка неотрицательна и оставляет видимую часть.
func (t Transform) validCrop() bool {
	for _, c := range []float64{t.CropLeft, t.CropTop, t.CropRight, t.CropBottom} {
		if c < 0 || math.IsNaN(c) || math.IsInf(c, 0) {
			return false
		}
	}
	if t.CropLeft == 0 && t.CropTop == 0 && t.CropRight == 0 && t.CropBottom == 0 {
		return true
	}
	return t.CropLeft+t.CropRight < t.Width && t.CropTop+t.CropBottom < t.Height
}

type Object struct {
	ID       string `json:"id"`
	Name     string `json:"name,omitempty"` // имя слоя; пусто — берётся имя source
	SourceID string `json:"sourceId"`
	// InstanceID связывает «один и тот же виджет» в разных layout (копия «со связью»): при смене сцены такой
	// iframe не перезагружается, а переносится на новое место. Пусто — виджеты сопоставляются по порядку внутри source.
	InstanceID string    `json:"instanceId,omitempty"`
	Transform  Transform `json:"transform"`
	Opacity    float64   `json:"opacity"`
	Visible    bool      `json:"visible"`
	Locked     bool      `json:"locked"`
	ZIndex     int       `json:"zIndex"`
}

type Canvas struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

// Layout — «как и где» показаны источники. Scenes — OBS-сцены, к которым он привязан (сцена принадлежит одному layout).
type Layout struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Scenes  []string `json:"scenes"`
	Canvas  Canvas   `json:"canvas"`
	Objects []Object `json:"objects"`
}

type SourceData struct {
	URL   string `json:"url,omitempty"`
	Proxy bool   `json:"proxy,omitempty"`
	// Для type "plugin": id плагина-виджета и его настройки (значения по схеме из plugin.json; передаются виджету в query).
	Plugin   string         `json:"plugin,omitempty"`
	Settings map[string]any `json:"settings,omitempty"`
}

// Source — «что показывать». В MVP только iframe.
type Source struct {
	ID   string     `json:"id"`
	Name string     `json:"name"`
	Type string     `json:"type"`
	Data SourceData `json:"data"`
}

// ObsInfo — что известно об OBS; уходит в UI и по WebSocket.
type ObsInfo struct {
	Connected       bool     `json:"connected"`
	Via             *string  `json:"via"` // "overlay" | "websocket" | null
	PermissionLevel *int     `json:"permissionLevel"`
	Scene           *string  `json:"scene"`
	Scenes          []string `json:"scenes"`
	Error           *string  `json:"error"`
}

// ObsMinPermission — минимальный уровень Page permissions Browser Source для getCurrentScene/getScenes.
const ObsMinPermission = 2

var idRe = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// IsValidID — id используется в именах/путях, поэтому строго ограничен.
func IsValidID(id string) bool { return idRe.MatchString(id) }

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

// Slugify превращает имя в id layout.
func Slugify(name string) string {
	s := strings.Trim(slugRe.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if len(s) > 40 {
		s = strings.Trim(s[:40], "-")
	}
	if s == "" {
		return "layout"
	}
	return s
}

// SafeURL допускает только http(s) и пути вида /path (но не //host).
func SafeURL(u string) bool {
	if u == "" {
		return false
	}
	if strings.HasPrefix(u, "/") {
		return !strings.HasPrefix(u, "//")
	}
	p, err := url.Parse(u)
	if err != nil {
		return false
	}
	return (p.Scheme == "http" || p.Scheme == "https") && p.Host != ""
}

var absURLRe = regexp.MustCompile(`(?i)^https?://`)

// IsAbsHTTP — для proxy нужен абсолютный http(s) URL.
func IsAbsHTTP(u string) bool { return absURLRe.MatchString(u) }

// NormalizeScenes: убирает пустые и повторы, режет длину и количество (порядок сохраняется).
func NormalizeScenes(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, s := range in {
		if s == "" || strings.TrimSpace(s) == "" {
			continue
		}
		if r := []rune(s); len(r) > 200 {
			s = string(r[:200])
		}
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
		if len(out) >= 200 {
			break
		}
	}
	return out
}

// Validate проверяет layout и приводит его к каноническому виду (nil-срезы → пустые, чтобы JSON отдавал [], а не null).
func (l *Layout) Validate() string {
	if !IsValidID(l.ID) {
		return "invalid id"
	}
	if strings.TrimSpace(l.Name) == "" {
		return "invalid name"
	}
	if l.Canvas.Width <= 0 || l.Canvas.Height <= 0 {
		return "invalid canvas"
	}
	l.Scenes = NormalizeScenes(l.Scenes)
	if l.Objects == nil {
		l.Objects = []Object{}
	}
	for _, o := range l.Objects {
		if o.InstanceID != "" && !IsValidID(o.InstanceID) {
			return "invalid instanceId"
		}
		if !o.Transform.validCrop() {
			return "invalid crop"
		}
	}
	return ""
}
