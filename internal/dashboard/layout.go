// Package dashboard — раскладка окна управления стримом (/): панели с виджетами плагинов области dashboard (#251).
// Пакет не знает ни про HTTP, ни про плагины: сервер подставляет доступные виджеты, клиент рисует панели.
//
// Раскладка — сетка из Cols колонок; у панели ширина W (в колонках) и высота H (в строках по RowPx пикселей), порядок панелей
// в массиве — порядок на экране (панели ложатся слева направо, затем вниз).
package dashboard

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Пределы.
const (
	Cols      = 12  // колонок сетки
	MaxRows   = 8   // наибольшая высота панели в строках
	RowPx     = 120 // высота одной строки сетки в пикселях (для клиента)
	MaxPanels = 16
	maxBytes  = 64 << 10
)

// Panel — одна панель окна: виджет плагина и размер.
type Panel struct {
	ID     string `json:"id"`
	Plugin string `json:"plugin"`
	Widget string `json:"widget"`
	W      int    `json:"w"` // колонок, 1..Cols
	H      int    `json:"h"` // строк, 1..MaxRows
}

// Layout — раскладка. Rev сервер увеличивает при каждом сохранении: два окна редактора не затрут правки друг друга.
type Layout struct {
	Rev    int     `json:"rev"`
	Panels []Panel `json:"panels"`
}

// Widget — виджет области dashboard, доступный для добавления (заполняет сервер из загруженных плагинов).
type Widget struct {
	Plugin string `json:"plugin"`
	Widget string `json:"widget"`
	Name   string `json:"name"`
}

var idRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// Parse разбирает и проверяет раскладку из JSON (неизвестные поля — ошибка).
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
	if l.Panels == nil {
		l.Panels = []Panel{} // в JSON — [], а не null
	}
	return l, l.Validate()
}

// Validate проверяет число и размеры панелей и уникальность их id. Существует ли виджет, не проверяется: плагин могли удалить,
// панель при этом остаётся в раскладке (окно покажет подсказку), а вернётся плагин — панель оживёт.
func (l *Layout) Validate() error {
	if len(l.Panels) > MaxPanels {
		return fmt.Errorf("panels: at most %d", MaxPanels)
	}
	seen := map[string]bool{}
	for _, p := range l.Panels {
		if !idRe.MatchString(p.ID) || seen[p.ID] {
			return fmt.Errorf("panel id %q: a-z, 0-9 and '-', unique", p.ID)
		}
		seen[p.ID] = true
		if !idRe.MatchString(p.Plugin) || !idRe.MatchString(p.Widget) {
			return fmt.Errorf("panel %q: plugin and widget ids expected", p.ID)
		}
		if p.W < 1 || p.W > Cols || p.H < 1 || p.H > MaxRows {
			return fmt.Errorf("panel %q: size must be 1..%d columns and 1..%d rows", p.ID, Cols, MaxRows)
		}
	}
	return nil
}

// Default — раскладка по умолчанию: каждый доступный виджет на половину ширины, высотой в 6 строк.
func Default(widgets []Widget) Layout {
	l := Layout{Panels: []Panel{}}
	for _, w := range widgets {
		if len(l.Panels) == MaxPanels {
			break
		}
		l.Panels = append(l.Panels, Panel{ID: w.Plugin + "-" + w.Widget, Plugin: w.Plugin, Widget: w.Widget, W: Cols / 2, H: 6})
	}
	return l
}
