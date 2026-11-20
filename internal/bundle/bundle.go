// Package bundle — экспорт и импорт настроек одним файлом oc-settings.json (основа синхронизации с облаком, #98).
//
// Файл версионный и состоит из независимых секций (layouts, deck, plugins…): каждая отдаёт свои данные, оценивает входящие (Plan — что будет
// создано, заменено, пропущено и что нельзя и почему) и применяет выбранное (Apply). Импорт никогда не применяется сразу: сначала Plan, затем
// пользователь выбирает элементы. Секреты (токены Twitch, пароли, ключи, устройства) в файл не попадают — это забота секций.
package bundle

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"
)

// Format — версия формата файла. Новее известной — отказ с понятным сообщением; старее — читаем.
const Format = 1

// MaxSize — предел размера читаемого файла (защита от мусора).
const MaxSize = 6 << 20 // меньше BodyLimit HTTP-сервера (8 МБ): файл уходит в теле запроса

// File — содержимое oc-settings.json.
type File struct {
	Format    int                        `json:"format"`
	App       string                     `json:"app,omitempty"` // версия OC, сделавшего файл
	CreatedAt string                     `json:"createdAt"`
	Sections  map[string]json.RawMessage `json:"sections"`
	// Checksums — sha256 компактной записи каждой секции: ловит порчу и ручную правку; не подпись.
	Checksums map[string]string `json:"checksums"`
}

// Статусы элемента плана.
const (
	StatusNew     = "new"     // такого нет — будет создан
	StatusReplace = "replace" // есть другой с тем же id — будет заменён (или добавлен копией)
	StatusSame    = "same"    // уже совпадает — ничего не меняется
	StatusProblem = "problem" // нельзя применить: причина в Reason
)

// Item — элемент секции в плане импорта (layout, источник, плагин…).
type Item struct {
	ID      string `json:"id"`
	Name    string `json:"name,omitempty"`
	Status  string `json:"status"`
	Reason  string `json:"reason,omitempty"` // у problem — почему нельзя; у остальных — предупреждение (необязательно)
	Warning bool   `json:"warning,omitempty"`
}

// Selectable — элемент можно выбрать для применения.
func (i Item) Selectable() bool { return i.Status != StatusProblem && i.Status != StatusSame }

// Режимы применения элемента.
const (
	ModeReplace = "replace" // создать или заменить
	ModeCopy    = "copy"    // добавить как копию под новым id (секции, где это возможно)
)

// Selection — что применять: секция → id элемента → режим. Нет в выборе — пропускается.
type Selection map[string]map[string]string

// Section — часть настроек (одна глобальная фича).
type Section interface {
	Name() string
	// Export отдаёт данные секции (JSON, без секретов).
	Export(ctx context.Context) (json.RawMessage, error)
	// Plan оценивает данные секции из файла, ничего не меняя.
	Plan(ctx context.Context, raw json.RawMessage) ([]Item, error)
	// Apply применяет выбранные элементы (id → режим) и возвращает отчёт по ним. Ошибка одного элемента не останавливает остальные.
	Apply(ctx context.Context, raw json.RawMessage, pick map[string]string) ([]Outcome, error)
}

// Outcome — итог по элементу.
type Outcome struct {
	ID    string `json:"id"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	Note  string `json:"note,omitempty"` // применено, но с оговоркой (что не перенесено)
}

// Service — набор секций в порядке применения.
type Service struct {
	App      string // версия OC для файла
	Now      func() time.Time
	sections []Section
	// BeforeApply вызывается перед первым изменением: локальный бэкап текущих настроек. Ошибка отменяет применение.
	BeforeApply func(ctx context.Context) error
}

// New — сервис с секциями; порядок — порядок применения (зависимости раньше зависимых: источники до layouts).
func New(app string, sections ...Section) *Service {
	return &Service{App: app, Now: time.Now, sections: sections}
}

// Names — имена секций по порядку.
func (s *Service) Names() []string {
	out := make([]string, len(s.sections))
	for i, sec := range s.sections {
		out[i] = sec.Name()
	}
	return out
}

func (s *Service) find(name string) Section {
	for _, sec := range s.sections {
		if sec.Name() == name {
			return sec
		}
	}
	return nil
}

func sum(raw json.RawMessage) (string, error) {
	var b bytes.Buffer
	if err := json.Compact(&b, raw); err != nil {
		return "", err
	}
	h := sha256.Sum256(b.Bytes())
	return hex.EncodeToString(h[:]), nil
}

// Export собирает файл из перечисленных секций (пусто — из всех).
func (s *Service) Export(ctx context.Context, names []string) (File, error) {
	f := File{Format: Format, App: s.App, CreatedAt: s.Now().UTC().Format(time.RFC3339), Sections: map[string]json.RawMessage{}, Checksums: map[string]string{}}
	want := map[string]bool{}
	for _, n := range names {
		if s.find(n) == nil {
			return f, fmt.Errorf("unknown section %q", n)
		}
		want[n] = true
	}
	for _, sec := range s.sections {
		if len(want) > 0 && !want[sec.Name()] {
			continue
		}
		raw, err := sec.Export(ctx)
		if err != nil {
			return f, fmt.Errorf("%s: %w", sec.Name(), err)
		}
		c, err := sum(raw)
		if err != nil {
			return f, fmt.Errorf("%s: %w", sec.Name(), err)
		}
		f.Sections[sec.Name()] = raw
		f.Checksums[sec.Name()] = c
	}
	return f, nil
}

// Ошибки разбора файла: текст показывается пользователю как есть.
var (
	ErrNotBundle = errors.New("this is not an Overlay Composer settings file")
	ErrTooNew    = errors.New("this file was made by a newer Overlay Composer — update the app to import it")
	ErrCorrupt   = errors.New("the settings file is damaged (checksum mismatch)")
)

// Parse разбирает и проверяет файл: размер, формат, версия, контрольные суммы. Неизвестные секции допустимы (их пропустим при плане).
func Parse(data []byte) (File, error) {
	var f File
	if len(data) > MaxSize {
		return f, errors.New("the settings file is too large")
	}
	if err := json.Unmarshal(data, &f); err != nil || f.Format == 0 || f.Sections == nil {
		return File{}, ErrNotBundle
	}
	if f.Format > Format {
		return File{}, ErrTooNew
	}
	for name, raw := range f.Sections {
		want, ok := f.Checksums[name]
		got, err := sum(raw)
		if err != nil || !ok || got != want {
			return File{}, fmt.Errorf("%w: section %q", ErrCorrupt, name)
		}
	}
	return f, nil
}

// SectionPlan — план по одной секции.
type SectionPlan struct {
	Name    string `json:"name"`
	Known   bool   `json:"known"`           // false — секцию не знает эта версия OC (будет пропущена)
	Items   []Item `json:"items,omitempty"` // для known
	Error   string `json:"error,omitempty"` // секцию не удалось оценить
	Present bool   `json:"present"`
}

// Plan оценивает файл по всем секциям, которые в нём есть (по порядку применения, затем неизвестные по имени). Ничего не меняет.
func (s *Service) Plan(ctx context.Context, f File) []SectionPlan {
	var out []SectionPlan
	for _, sec := range s.sections {
		raw, ok := f.Sections[sec.Name()]
		if !ok {
			continue
		}
		p := SectionPlan{Name: sec.Name(), Known: true, Present: true}
		items, err := sec.Plan(ctx, raw)
		if err != nil {
			p.Error = err.Error()
		}
		p.Items = items
		out = append(out, p)
	}
	var unknown []string
	for name := range f.Sections {
		if s.find(name) == nil {
			unknown = append(unknown, name)
		}
	}
	sort.Strings(unknown)
	for _, name := range unknown {
		out = append(out, SectionPlan{Name: name, Present: true})
	}
	return out
}

// SectionReport — итог применения секции.
type SectionReport struct {
	Name     string    `json:"name"`
	Outcomes []Outcome `json:"outcomes,omitempty"`
	Error    string    `json:"error,omitempty"`
}

// Apply применяет выбранное по секциям в порядке сервиса. Перед первым изменением — BeforeApply (бэкап); его ошибка отменяет всё.
// Ошибка одной секции не откатывает остальные — она попадает в отчёт. Выбор по неизвестной секции или элементу вне плана — ошибка.
func (s *Service) Apply(ctx context.Context, f File, sel Selection) ([]SectionReport, error) {
	for name, pick := range sel {
		if s.find(name) == nil {
			return nil, fmt.Errorf("unknown section %q", name)
		}
		if _, ok := f.Sections[name]; !ok && len(pick) > 0 {
			return nil, fmt.Errorf("section %q is not in the file", name)
		}
		for _, mode := range pick {
			if mode != ModeReplace && mode != ModeCopy {
				return nil, fmt.Errorf("section %q: unknown mode %q", name, mode)
			}
		}
	}
	// выбранное должно быть в плане и применимо: так в Apply не попадёт то, что модалка показывала красным
	plans := map[string]map[string]Item{}
	for _, p := range s.Plan(ctx, f) {
		if p.Known {
			m := map[string]Item{}
			for _, it := range p.Items {
				m[it.ID] = it
			}
			plans[p.Name] = m
		}
	}
	picked := false
	for name, pick := range sel {
		for id := range pick {
			it, ok := plans[name][id]
			if !ok {
				return nil, fmt.Errorf("%s: %q is not in the file", name, id)
			}
			if !it.Selectable() {
				return nil, fmt.Errorf("%s: %q cannot be imported: %s", name, id, it.Reason)
			}
			picked = true
		}
	}
	if !picked {
		return []SectionReport{}, nil
	}
	if s.BeforeApply != nil {
		if err := s.BeforeApply(ctx); err != nil {
			return nil, fmt.Errorf("could not back up the current settings, nothing was changed: %w", err)
		}
	}
	reports := []SectionReport{}
	for _, sec := range s.sections {
		pick := sel[sec.Name()]
		if len(pick) == 0 {
			continue
		}
		r := SectionReport{Name: sec.Name()}
		out, err := sec.Apply(ctx, f.Sections[sec.Name()], pick)
		r.Outcomes = out
		if err != nil {
			r.Error = err.Error()
		}
		reports = append(reports, r)
	}
	return reports, nil
}
