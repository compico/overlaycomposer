// Package deck — раскладка панели управления для старых устройств (iPad/Safari 9): модель, проверка и отрисовка на сервере.
// Пакет не знает ни про OBS, ни про HTTP: сервер подставляет сцены и состояние, а клиент получает готовый HTML.
package deck

import (
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Сетка и пределы. Сетка рассчитана на iPad 3 (1024×768 CSS px) в альбомной ориентации.
const (
	Cols         = 8
	Rows         = 6
	MaxPages     = 8
	MaxTilesPage = Cols * Rows
	MaxTiles     = 200
	MaxLabel     = 40
	MaxName      = 24
	maxBytes     = 256 << 10
)

// Типы действий плитки. Действие определяет сервер по сохранённой раскладке; клиент присылает только id плитки.
const (
	ActionScene = "scene" // сменить сцену OBS (Scene)
	ActionPage  = "page"  // перейти на страницу раскладки (Page)
)

var idRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// Palette — цвета Metro: клиенту уходит имя, а не произвольный CSS (нечего внедрять в стиль).
var Palette = map[string]string{
	"blue": "#0050ef", "cyan": "#1ba1e2", "teal": "#00aba9", "green": "#60a917", "lime": "#a4c400", "yellow": "#e3c800",
	"orange": "#fa6800", "red": "#e51400", "magenta": "#d80073", "purple": "#aa00ff", "steel": "#647687", "brown": "#825a2c",
}

// Icons — глифы, которые умеют показывать и старые устройства (обычные символы Unicode, без шрифтов и картинок).
var Icons = map[string]string{
	"scene": "▣", "page": "»", "home": "⌂", "play": "▶", "pause": "❚❚", "next": "▶▶", "ban": "⊘", "mic": "●",
}

type Layout struct {
	// Rev растёт при каждом сохранении; устройство сравнивает его со своим и перерисовывается.
	Rev   int    `json:"rev"`
	Pages []Page `json:"pages"`
}

type Page struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Tiles []Tile `json:"tiles"`
}

type Tile struct {
	ID    string `json:"id"` // уникален во всей раскладке: нажатие приходит с одним id
	X     int    `json:"x"`  // клетки, с 0
	Y     int    `json:"y"`
	W     int    `json:"w"`
	H     int    `json:"h"`
	Label string `json:"label,omitempty"`
	Color string `json:"color,omitempty"`
	Icon  string `json:"icon,omitempty"`
	// Confirm — опасное действие: сервер выполняет его только по второму касанию.
	Confirm bool   `json:"confirm,omitempty"`
	Action  Action `json:"action"`
}

type Action struct {
	Type  string `json:"type"`
	Scene string `json:"scene,omitempty"`
	Page  string `json:"page,omitempty"`
}

// Parse читает раскладку из JSON и проверяет её (неизвестные поля — ошибка: опечатка не должна молча терять настройку).
func Parse(raw []byte) (Layout, error) {
	var l Layout
	if len(raw) > maxBytes {
		return l, errors.New("layout is too large")
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&l); err != nil {
		return l, fmt.Errorf("bad layout: %v", err)
	}
	return l, l.Validate()
}

// Validate проверяет границы, уникальность id, пересечения плиток и допустимые значения.
func (l *Layout) Validate() error {
	if len(l.Pages) == 0 || len(l.Pages) > MaxPages {
		return fmt.Errorf("pages: 1..%d", MaxPages)
	}
	pages := map[string]bool{}
	for _, p := range l.Pages {
		if !idRe.MatchString(p.ID) || pages[p.ID] {
			return fmt.Errorf("page id %q: a-z, 0-9, -, up to 32 characters, unique", p.ID)
		}
		pages[p.ID] = true
		if n := utf8.RuneCountInString(p.Name); n == 0 || n > MaxName {
			return fmt.Errorf("page %q: name 1..%d characters", p.ID, MaxName)
		}
	}
	tiles, total := map[string]bool{}, 0
	for _, p := range l.Pages {
		if len(p.Tiles) > MaxTilesPage {
			return fmt.Errorf("page %q: at most %d tiles", p.ID, MaxTilesPage)
		}
		var grid [Rows][Cols]bool
		for _, t := range p.Tiles {
			if total++; total > MaxTiles {
				return fmt.Errorf("at most %d tiles", MaxTiles)
			}
			if !idRe.MatchString(t.ID) || tiles[t.ID] {
				return fmt.Errorf("tile id %q: a-z, 0-9, -, up to 32 characters, unique", t.ID)
			}
			tiles[t.ID] = true
			if t.W < 1 || t.H < 1 || t.W > 4 || t.H > 4 || t.X < 0 || t.Y < 0 || t.X+t.W > Cols || t.Y+t.H > Rows {
				return fmt.Errorf("tile %q: outside the %d×%d grid", t.ID, Cols, Rows)
			}
			for y := t.Y; y < t.Y+t.H; y++ {
				for x := t.X; x < t.X+t.W; x++ {
					if grid[y][x] {
						return fmt.Errorf("tile %q overlaps another tile", t.ID)
					}
					grid[y][x] = true
				}
			}
			if utf8.RuneCountInString(t.Label) > MaxLabel || strings.ContainsAny(t.Label, "\r\n\t") {
				return fmt.Errorf("tile %q: label up to %d characters, one line", t.ID, MaxLabel)
			}
			if t.Color != "" && Palette[t.Color] == "" {
				return fmt.Errorf("tile %q: unknown color %q", t.ID, t.Color)
			}
			if t.Icon != "" && Icons[t.Icon] == "" {
				return fmt.Errorf("tile %q: unknown icon %q", t.ID, t.Icon)
			}
		}
	}
	// действия проверяем после того, как известны все страницы
	for _, p := range l.Pages {
		for _, t := range p.Tiles {
			switch t.Action.Type {
			case ActionScene:
				if n := utf8.RuneCountInString(t.Action.Scene); n == 0 || n > 200 {
					return fmt.Errorf("tile %q: scene name 1..200 characters", t.ID)
				}
			case ActionPage:
				if !pages[t.Action.Page] {
					return fmt.Errorf("tile %q: page %q does not exist", t.ID, t.Action.Page)
				}
			default:
				return fmt.Errorf("tile %q: unknown action %q", t.ID, t.Action.Type)
			}
		}
	}
	return nil
}

// Tile находит плитку по id во всей раскладке.
func (l *Layout) Tile(id string) (Tile, bool) {
	for _, p := range l.Pages {
		for _, t := range p.Tiles {
			if t.ID == id {
				return t, true
			}
		}
	}
	return Tile{}, false
}

// Page находит страницу; пустой id — первая.
func (l *Layout) Page(id string) (Page, bool) {
	for _, p := range l.Pages {
		if id == "" || p.ID == id {
			return p, true
		}
	}
	return Page{}, false
}

// Default — раскладка по умолчанию, пока пользователь ничего не сохранил: по плитке 2×2 на каждую сцену OBS, всё на страницах по 8.
// Id плитки зависит от имени сцены, а не от порядка: после перестановки сцен нажатие на старой странице не перепутает сцену.
func Default(scenes []string) Layout {
	const perPage = 8 // 4 плитки в ряд × 2 ряда по 2×2 в сетке 8×6 — оставляет место под подпись и крупные пальцы
	var pages []Page
	colors := []string{"blue", "teal", "green", "orange", "purple", "magenta", "steel", "red"}
	seen := map[string]bool{}
	for i, name := range scenes {
		id := "scene-" + shortHash(name)
		if seen[id] {
			continue
		}
		seen[id] = true
		if i/perPage >= MaxPages {
			break
		}
		if i%perPage == 0 {
			n := i/perPage + 1
			pages = append(pages, Page{ID: fmt.Sprintf("p%d", n), Name: fmt.Sprintf("Scenes %d", n)})
		}
		p := &pages[len(pages)-1]
		k := len(p.Tiles)
		p.Tiles = append(p.Tiles, Tile{
			ID: id, X: k % 4 * 2, Y: k / 4 * 2, W: 2, H: 2, Label: trunc(name, MaxLabel), Color: colors[k%len(colors)], Icon: "scene",
			Action: Action{Type: ActionScene, Scene: name},
		})
	}
	if len(pages) == 0 {
		pages = []Page{{ID: "p1", Name: "Scenes 1"}}
	}
	return Layout{Pages: pages}
}

func shortHash(s string) string {
	h := fnv.New32a()
	h.Write([]byte(s))
	return fmt.Sprintf("%08x", h.Sum32())
}

func trunc(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}
