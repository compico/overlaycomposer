// Package deck — раскладка панели управления для старых устройств (iPad/Safari 9): модель, проверка и отрисовка на сервере.
// Пакет не знает ни про OBS, ни про HTTP: сервер подставляет сцены и состояние, а клиент получает готовый HTML.
//
// Раскладка в духе начального экрана Windows 8: квадратная клетка, плитки кратных размеров, плитки собраны в группы с заголовками,
// группы идут слева направо, длинная раскладка прокручивается вправо. Рядов всегда Rows: высота экрана делится на ряды, поэтому
// поворот устройства меняет только размер клетки и количество видимых колонок, а не саму раскладку.
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

// Пределы.
const (
	Rows         = 4  // рядов клеток; по ним делится высота экрана
	MaxGroupCols = 16 // колонок в одной группе
	MaxGroups    = 16
	MaxTiles     = 200
	MaxLabel     = 40
	MaxName      = 24
	maxBytes     = 256 << 10
)

// Size — допустимый размер плитки в клетках (ширина × высота). 1×1 — обычная, 2×1 и 3×1 — широкие (название трека, длинные подписи),
// 2×3 — крупная для редких случаев.
type Size struct {
	W int `json:"w"`
	H int `json:"h"`
}

var Sizes = []Size{{W: 1, H: 1}, {W: 2, H: 1}, {W: 3, H: 1}, {W: 2, H: 3}}

func sizeOK(w, h int) bool {
	for _, s := range Sizes {
		if s.W == w && s.H == h {
			return true
		}
	}
	return false
}

// Типы действий плитки. Действие определяет сервер по сохранённой раскладке; клиент присылает только id плитки.
const (
	ActionScene = "scene" // сменить сцену OBS (Scene)
)

var idRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// Palette — цвета Metro: клиенту уходит имя, а не произвольный CSS (нечего внедрять в стиль).
var Palette = map[string]string{
	"blue": "#0050ef", "cyan": "#1ba1e2", "teal": "#00aba9", "green": "#60a917", "lime": "#a4c400", "yellow": "#e3c800",
	"orange": "#fa6800", "red": "#e51400", "magenta": "#d80073", "purple": "#aa00ff", "steel": "#647687", "brown": "#825a2c",
}

// Icons — глифы, которые умеют показывать и старые устройства (обычные символы Unicode, без шрифтов и картинок).
var Icons = map[string]string{
	"scene": "▣", "home": "⌂", "play": "▶", "pause": "❚❚", "next": "▶▶", "ban": "⊘", "mic": "●",
}

type Layout struct {
	// Rev растёт при каждом сохранении; устройство сравнивает его со своим и перерисовывается.
	Rev    int     `json:"rev"`
	Groups []Group `json:"groups"`
}

// Group — набор плиток с заголовком. Плитки лежат на сетке группы: x от 0 вправо (ширина группы — по самой правой плитке), y от 0 вниз до Rows.
type Group struct {
	ID    string `json:"id"`
	Name  string `json:"name,omitempty"`
	Tiles []Tile `json:"tiles"`
}

type Tile struct {
	ID    string `json:"id"` // уникален во всей раскладке: нажатие приходит с одним id
	X     int    `json:"x"`  // клетки внутри группы, с 0
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
}

// Width — ширина группы в клетках (минимум 1, чтобы пустая группа оставалась видимой и её можно было наполнить).
func (g Group) Width() int {
	w := 1
	for _, t := range g.Tiles {
		if t.X+t.W > w {
			w = t.X + t.W
		}
	}
	return w
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
	for i := range l.Groups {
		if l.Groups[i].Tiles == nil {
			l.Groups[i].Tiles = []Tile{} // в JSON — [], а не null: клиенту не нужно проверять
		}
	}
	return l, l.Validate()
}

// Validate проверяет границы, уникальность id, пересечения плиток и допустимые значения.
func (l *Layout) Validate() error {
	if len(l.Groups) == 0 || len(l.Groups) > MaxGroups {
		return fmt.Errorf("groups: 1..%d", MaxGroups)
	}
	groups, tiles, total := map[string]bool{}, map[string]bool{}, 0
	for _, g := range l.Groups {
		if !idRe.MatchString(g.ID) || groups[g.ID] {
			return fmt.Errorf("group id %q: a-z, 0-9, -, up to 32 characters, unique", g.ID)
		}
		groups[g.ID] = true
		if utf8.RuneCountInString(g.Name) > MaxName || strings.ContainsAny(g.Name, "\r\n\t") {
			return fmt.Errorf("group %q: name up to %d characters, one line", g.ID, MaxName)
		}
		var grid [Rows][MaxGroupCols]bool
		for _, t := range g.Tiles {
			if total++; total > MaxTiles {
				return fmt.Errorf("at most %d tiles", MaxTiles)
			}
			if !idRe.MatchString(t.ID) || tiles[t.ID] {
				return fmt.Errorf("tile id %q: a-z, 0-9, -, up to 32 characters, unique", t.ID)
			}
			tiles[t.ID] = true
			if !sizeOK(t.W, t.H) {
				return fmt.Errorf("tile %q: size %d×%d is not allowed (1×1, 2×1, 3×1, 2×3)", t.ID, t.W, t.H)
			}
			if t.X < 0 || t.Y < 0 || t.X+t.W > MaxGroupCols || t.Y+t.H > Rows {
				return fmt.Errorf("tile %q: outside the group (up to %d columns, %d rows)", t.ID, MaxGroupCols, Rows)
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
			switch t.Action.Type {
			case ActionScene:
				if n := utf8.RuneCountInString(t.Action.Scene); n == 0 || n > 200 {
					return fmt.Errorf("tile %q: scene name 1..200 characters", t.ID)
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
	for _, g := range l.Groups {
		for _, t := range g.Tiles {
			if t.ID == id {
				return t, true
			}
		}
	}
	return Tile{}, false
}

// Default — раскладка по умолчанию, пока пользователь ничего не сохранил: широкие плитки 2×1 на каждую сцену OBS в порядке OBS,
// столбцами по Rows штук, группами по 8 сцен. Id плитки зависит от имени сцены, а не от порядка: после перестановки сцен нажатие на старой
// странице не перепутает сцену.
func Default(scenes []string) Layout {
	const perGroup = 2 * Rows
	colors := []string{"blue", "teal", "green", "orange", "purple", "magenta", "steel", "red"}
	var groups []Group
	seen := map[string]bool{}
	n := 0
	for _, name := range scenes {
		id := "scene-" + shortHash(name)
		if seen[id] {
			continue
		}
		seen[id] = true
		if n/perGroup >= MaxGroups {
			break
		}
		if n%perGroup == 0 {
			k := n/perGroup + 1
			title := "Scenes"
			if k > 1 {
				title = fmt.Sprintf("Scenes %d", k)
			}
			groups = append(groups, Group{ID: fmt.Sprintf("g%d", k), Name: title})
		}
		g := &groups[len(groups)-1]
		k := n % perGroup
		g.Tiles = append(g.Tiles, Tile{
			ID: id, X: k / Rows * 2, Y: k % Rows, W: 2, H: 1, Label: trunc(name, MaxLabel), Color: colors[k%len(colors)],
			Action: Action{Type: ActionScene, Scene: name},
		})
		n++
	}
	if len(groups) == 0 {
		groups = []Group{{ID: "g1", Name: "Scenes"}}
	}
	return Layout{Groups: groups}
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
