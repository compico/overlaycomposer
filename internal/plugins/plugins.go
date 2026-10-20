// Package plugins — плагины-виджеты. Плагин — подпапка с plugin.json (манифест + схема настроек) и файлами виджета
// (HTML/JS/CSS/картинки). Сервер раздаёт их по /plugins/<id>/…, а настройки источника передаёт виджету в query-параметрах.
//
// Источники плагинов: встроенные (в бинарнике) и папка plugins/ рядом с данными приложения. Папка пересматривается при
// каждом обращении к списку, поэтому новый плагин появляется без перезапуска. Встроенный плагин нельзя переопределить.
package plugins

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"math"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/compico/overlaycomposer/internal/model"
)

const ManifestFile = "plugin.json"

// Допустимые типы настроек.
const (
	TString = "string"
	TText   = "text"
	TNumber = "number"
	TRange  = "range"
	TBool   = "bool"
	TColor  = "color"
	TFont   = "font"
	TSelect = "select"
)

var settingTypes = map[string]bool{TString: true, TText: true, TNumber: true, TRange: true, TBool: true, TColor: true, TFont: true, TSelect: true}

type Option struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// Setting — одна настройка виджета; редактор строит по схеме форму, значения уходят виджету как ?key=value.
type Setting struct {
	Key     string   `json:"key"`
	Label   string   `json:"label"`
	Type    string   `json:"type"`
	Group   string   `json:"group,omitempty"`
	Help    string   `json:"help,omitempty"`
	Default any      `json:"default,omitempty"`
	Options []Option `json:"options,omitempty"` // для select
	Min     *float64 `json:"min,omitempty"`     // для number/range
	Max     *float64 `json:"max,omitempty"`
	Step    *float64 `json:"step,omitempty"`
}

type Size struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

type Manifest struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Version     string    `json:"version,omitempty"`
	Description string    `json:"description,omitempty"`
	Author      string    `json:"author,omitempty"`
	Entry       string    `json:"entry,omitempty"`       // по умолчанию index.html
	DefaultSize Size      `json:"defaultSize,omitempty"` // размер при добавлении на layout
	Settings    []Setting `json:"settings,omitempty"`
	Builtin     bool      `json:"builtin"` // заполняется загрузчиком
}

var settingKeyRe = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]{0,31}$`)

// Validate проверяет и нормализует манифест.
func (m *Manifest) Validate() error {
	if !model.IsValidID(m.ID) {
		return errors.New("invalid id")
	}
	if strings.TrimSpace(m.Name) == "" {
		return errors.New("name is required")
	}
	if m.Entry == "" {
		m.Entry = "index.html"
	}
	if !fs.ValidPath(m.Entry) || m.Entry == "." || strings.HasSuffix(m.Entry, "/") {
		return fmt.Errorf("invalid entry %q", m.Entry)
	}
	if m.DefaultSize.Width <= 0 || m.DefaultSize.Height <= 0 {
		m.DefaultSize = Size{Width: 400, Height: 300}
	}
	seen := map[string]bool{}
	for i := range m.Settings {
		s := &m.Settings[i]
		if !settingKeyRe.MatchString(s.Key) || seen[s.Key] {
			return fmt.Errorf("setting %d: invalid or duplicate key %q", i, s.Key)
		}
		seen[s.Key] = true
		if !settingTypes[s.Type] {
			return fmt.Errorf("setting %q: unknown type %q", s.Key, s.Type)
		}
		if s.Label == "" {
			s.Label = s.Key
		}
		if s.Type == TSelect && len(s.Options) == 0 {
			return fmt.Errorf("setting %q: select needs options", s.Key)
		}
	}
	return nil
}

// Setting возвращает схему настройки по ключу.
func (m *Manifest) setting(key string) *Setting {
	for i := range m.Settings {
		if m.Settings[i].Key == key {
			return &m.Settings[i]
		}
	}
	return nil
}

const maxText = 2000

// CleanSettings оставляет только известные настройки и приводит значения к типам схемы
// (число — в пределах min/max, select — из списка, цвет — #rgb/#rrggbb/#rrggbbaa, строки — ограниченной длины).
// Некорректное значение — ошибка (редактор показывает её, а не молча теряет ввод).
func (m *Manifest) CleanSettings(in map[string]any) (map[string]any, error) {
	out := make(map[string]any, len(in))
	for k, v := range in {
		s := m.setting(k)
		if s == nil {
			continue // настройка из другой версии плагина — отбрасываем
		}
		cv, err := cleanValue(s, v)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", k, err)
		}
		out[k] = cv
	}
	return out, nil
}

var colorRe = regexp.MustCompile(`^#([0-9a-fA-F]{3}|[0-9a-fA-F]{6}|[0-9a-fA-F]{8})$`)

func cleanValue(s *Setting, v any) (any, error) {
	switch s.Type {
	case TBool:
		b, ok := v.(bool)
		if !ok {
			return nil, errors.New("boolean expected")
		}
		return b, nil
	case TNumber, TRange:
		f, ok := v.(float64)
		if !ok || math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, errors.New("number expected")
		}
		if s.Min != nil && f < *s.Min {
			f = *s.Min
		}
		if s.Max != nil && f > *s.Max {
			f = *s.Max
		}
		return f, nil
	}
	str, ok := v.(string)
	if !ok {
		return nil, errors.New("string expected")
	}
	if len([]rune(str)) > maxText {
		return nil, errors.New("too long")
	}
	switch s.Type {
	case TColor:
		if !colorRe.MatchString(str) {
			return nil, errors.New("color must look like #rrggbb")
		}
	case TSelect:
		for _, o := range s.Options {
			if o.Value == str {
				return str, nil
			}
		}
		return nil, errors.New("not one of the options")
	}
	return str, nil
}

// Plugin — найденный плагин: манифест и его файлы.
type Plugin struct {
	Manifest
	files fs.FS
}

// Open открывает файл плагина по относительному пути (только внутри папки плагина; каталог → entry).
func (p *Plugin) Open(rel string) (fs.File, string, error) {
	rel = strings.TrimPrefix(path.Clean("/"+rel), "/")
	if rel == "" || rel == "." {
		rel = p.Entry
	}
	if !fs.ValidPath(rel) {
		return nil, "", fs.ErrNotExist
	}
	f, err := p.files.Open(rel)
	if err != nil {
		return nil, "", err
	}
	if st, err := f.Stat(); err != nil || st.IsDir() {
		f.Close()
		return nil, "", fs.ErrNotExist
	}
	return f, rel, nil
}

type Registry struct {
	builtin fs.FS
	dir     fs.FS // nil — папки нет

	mu   sync.RWMutex
	list []*Plugin
}

// New: builtin — корень со встроенными плагинами (подпапка на плагин), dir — пользовательские (может быть nil).
func New(builtin, dir fs.FS) *Registry {
	r := &Registry{builtin: builtin, dir: dir}
	r.Scan()
	return r
}

// Scan перечитывает плагины. Ошибки в отдельных плагинах пишутся в лог и не мешают остальным.
func (r *Registry) Scan() {
	var found []*Plugin
	seen := map[string]bool{}
	load := func(root fs.FS, builtin bool) {
		if root == nil {
			return
		}
		entries, err := fs.ReadDir(root, ".")
		if err != nil {
			return
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			p, err := loadPlugin(root, e.Name(), builtin)
			if err != nil {
				slog.Warn("plugins: плагин пропущен", "dir", e.Name(), "err", err)
				continue
			}
			if seen[p.ID] {
				slog.Warn("plugins: повторный id, плагин пропущен", "id", p.ID, "dir", e.Name())
				continue
			}
			seen[p.ID] = true
			found = append(found, p)
		}
	}
	load(r.builtin, true) // встроенные первыми: их id занят
	load(r.dir, false)
	sort.SliceStable(found, func(i, j int) bool {
		if found[i].Builtin != found[j].Builtin {
			return found[i].Builtin
		}
		return strings.ToLower(found[i].Name) < strings.ToLower(found[j].Name)
	})
	r.mu.Lock()
	r.list = found
	r.mu.Unlock()
}

func loadPlugin(root fs.FS, dir string, builtin bool) (*Plugin, error) {
	sub, err := fs.Sub(root, dir)
	if err != nil {
		return nil, err
	}
	raw, err := fs.ReadFile(sub, ManifestFile)
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("%s: %w", ManifestFile, err)
	}
	m.Builtin = builtin
	if err := m.Validate(); err != nil {
		return nil, err
	}
	if st, err := fs.Stat(sub, m.Entry); err != nil || st.IsDir() {
		return nil, fmt.Errorf("entry %q not found", m.Entry)
	}
	return &Plugin{Manifest: m, files: sub}, nil
}

// List — манифесты всех плагинов (после повторного сканирования папки).
func (r *Registry) List() []Manifest {
	r.Scan()
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Manifest, 0, len(r.list))
	for _, p := range r.list {
		out = append(out, p.Manifest)
	}
	return out
}

// Get возвращает плагин по id; если не найден — один раз пересканирует папку (плагин могли только что положить).
func (r *Registry) Get(id string) (*Plugin, bool) {
	if p, ok := r.find(id); ok {
		return p, true
	}
	r.Scan()
	return r.find(id)
}

func (r *Registry) find(id string) (*Plugin, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, p := range r.list {
		if p.ID == id {
			return p, true
		}
	}
	return nil, false
}
