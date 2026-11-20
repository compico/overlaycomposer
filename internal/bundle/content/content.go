// Package content — секции настроек «источники» (sources) и «layouts» для internal/bundle (#192).
//
// Хранилище и оповещения приходят снаружи (интерфейсы), поэтому секции проверяются тестами без базы. Принципы импорта:
//   - всё, что приходит из файла, проверяется теми же правилами, что и создание через API; негодное — problem с причиной;
//   - права плагинов (Granted у источника) не переносятся: их выдаёт пользователь заново;
//   - layout с привязками к сценам, занятым другим layout, забирает сцены себе — об этом предупреждение в плане;
//   - layout по умолчанию и привязки в файле сохраняются для информации, но локальный выбор по умолчанию не меняется.
package content

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/compico/overlaycomposer/internal/bundle"
	"github.com/compico/overlaycomposer/internal/model"
)

// SourceStore — то, что нужно секции источников от хранилища.
type SourceStore interface {
	ListSources() ([]model.Source, error)
	SaveSource(model.Source) error
}

// LayoutStore — то, что нужно секции layouts от хранилища.
type LayoutStore interface {
	ListLayouts() ([]model.Layout, error)
	SaveLayout(model.Layout) (stolen bool, err error)
}

// PluginSettings проверяет значения настроек плагина по его схеме. known=false — плагина (пока) нет: тогда остаются только простые значения.
type PluginSettings func(plugin, widget string, in map[string]any) (out map[string]any, known bool, err error)

// maxItems — предел элементов секции (защита от мусора).
const maxItems = 2000

func canonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

// ---- sources ----

type sourcesData struct {
	Sources []model.Source `json:"sources"`
}

// Sources — секция «sources».
type Sources struct {
	Store    SourceStore
	Settings PluginSettings // nil — любой плагин считается неизвестным
	Changed  func()         // после применения (оповестить клиентов); необязательно
}

func (*Sources) Name() string { return "sources" }

func (s *Sources) Export(context.Context) (json.RawMessage, error) {
	list, err := s.Store.ListSources()
	if err != nil {
		return nil, err
	}
	out := sourcesData{Sources: make([]model.Source, 0, len(list))}
	for _, src := range list {
		src.Data.Granted = nil // права выдаёт пользователь, в файл они не идут
		out.Sources = append(out.Sources, src)
	}
	return json.Marshal(out)
}

// clean приводит источник из файла к безопасному виду. warn — оговорка, err — применить нельзя.
func (s *Sources) clean(src model.Source) (model.Source, string, error) {
	if !model.IsValidID(src.ID) || strings.TrimSpace(src.Name) == "" || (src.Type != "iframe" && src.Type != "plugin") {
		return src, "", errors.New("invalid source (id, name or type)")
	}
	if src.Type == "iframe" {
		if !model.SafeURL(src.Data.URL) {
			return src, "", errors.New("invalid url (only http/https or /path)")
		}
		if src.Data.Proxy && !model.IsAbsHTTP(src.Data.URL) {
			return src, "", errors.New("proxy needs an absolute http(s) URL")
		}
		if !model.ValidRouter(src.Data.Router) {
			return src, "", errors.New("invalid router")
		}
		src.Data = model.SourceData{URL: src.Data.URL, Proxy: src.Data.Proxy, Router: src.Data.Router}
		return src, "", nil
	}
	if src.Data.Plugin == "" || !model.IsValidID(src.Data.Plugin) || (src.Data.Widget != "" && !model.IsValidID(src.Data.Widget)) {
		return src, "", errors.New("invalid plugin id")
	}
	var settings map[string]any
	var known bool
	if s.Settings != nil {
		var err error
		if settings, known, err = s.Settings(src.Data.Plugin, src.Data.Widget, src.Data.Settings); err != nil {
			return src, "", fmt.Errorf("invalid settings: %w", err)
		}
	}
	warn := ""
	if !known {
		settings = simpleSettings(src.Data.Settings)
		warn = "Plugin " + src.Data.Plugin + " is not installed yet: install it, then check this widget's settings."
	}
	src.Data = model.SourceData{Plugin: src.Data.Plugin, Widget: src.Data.Widget, Settings: settings}
	return src, warn, nil
}

// simpleSettings оставляет только короткие скалярные значения: схемы плагина нет, чтобы проверить остальное.
func simpleSettings(in map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range in {
		switch x := v.(type) {
		case bool, float64:
			out[k] = x
		case string:
			if len(x) <= 1000 {
				out[k] = x
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (s *Sources) parse(raw json.RawMessage) ([]model.Source, error) {
	var in sourcesData
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, errors.New("the sources section is unreadable")
	}
	if len(in.Sources) > maxItems {
		return nil, errors.New("too many sources")
	}
	return in.Sources, nil
}

func (s *Sources) Plan(_ context.Context, raw json.RawMessage) ([]bundle.Item, error) {
	in, err := s.parse(raw)
	if err != nil {
		return nil, err
	}
	have, err := s.Store.ListSources()
	if err != nil {
		return nil, err
	}
	cur := map[string]model.Source{}
	for _, h := range have {
		cur[h.ID] = h
	}
	items := make([]bundle.Item, 0, len(in))
	seen := map[string]bool{}
	for _, src := range in {
		it := bundle.Item{ID: src.ID, Name: src.Name}
		cl, warn, err := s.clean(src)
		switch c, exists := cur[src.ID]; {
		case seen[src.ID]:
			it.Status, it.Reason = bundle.StatusProblem, "The file lists this id twice."
		case err != nil:
			it.Status, it.Reason = bundle.StatusProblem, err.Error()
		case exists && canonEqual(stripGranted(c), cl):
			it.Status = bundle.StatusSame
		case exists:
			it.Status = bundle.StatusReplace
			it.Reason, it.Warning = "Replaces the source with this id. Permissions granted to it stay as they are.", true
		default:
			it.Status = bundle.StatusNew
		}
		if warn != "" && it.Status != bundle.StatusProblem {
			it.Reason, it.Warning = strings.TrimSpace(it.Reason+" "+warn), true
		}
		seen[src.ID] = true
		items = append(items, it)
	}
	return items, nil
}

func stripGranted(src model.Source) model.Source {
	src.Data.Granted = nil
	return src
}

func (s *Sources) Apply(_ context.Context, raw json.RawMessage, pick map[string]string) ([]bundle.Outcome, error) {
	in, err := s.parse(raw)
	if err != nil {
		return nil, err
	}
	have, err := s.Store.ListSources()
	if err != nil {
		return nil, err
	}
	cur := map[string]model.Source{}
	for _, h := range have {
		cur[h.ID] = h
	}
	var out []bundle.Outcome
	for _, src := range in {
		mode, ok := pick[src.ID]
		if !ok {
			continue
		}
		o := bundle.Outcome{ID: src.ID}
		cl, _, err := s.clean(src)
		switch {
		case mode != bundle.ModeReplace:
			o.Error = "sources are imported by replacing only"
		case err != nil:
			o.Error = err.Error()
		default:
			if old, exists := cur[src.ID]; exists {
				cl.Data.Granted = old.Data.Granted // права остаются: источник заменяется, а не выдаётся заново
				if cl.Data.Plugin != old.Data.Plugin {
					cl.Data.Granted = nil // другой плагин — права прежнего ему не принадлежат
				}
			}
			if err := s.Store.SaveSource(cl); err != nil {
				o.Error = err.Error()
			} else {
				o.OK = true
			}
		}
		out = append(out, o)
	}
	if s.Changed != nil && len(out) > 0 {
		s.Changed()
	}
	return out, nil
}

// ---- layouts ----

type layoutsData struct {
	Layouts []model.Layout `json:"layouts"`
	Default string         `json:"default,omitempty"` // для информации: локальный layout по умолчанию не меняется
}

// Layouts — секция «layouts».
type Layouts struct {
	Store   LayoutStore
	Sources SourceStore // чтобы убрать из layout объекты с несуществующим источником; nil — не проверять
	Default func() string
	Changed func(ids []string) // после применения: id сохранённых layout (оповестить клиентов); необязательно
}

func (*Layouts) Name() string { return "layouts" }

func (l *Layouts) Export(context.Context) (json.RawMessage, error) {
	list, err := l.Store.ListLayouts()
	if err != nil {
		return nil, err
	}
	out := layoutsData{Layouts: make([]model.Layout, 0, len(list))}
	for _, x := range list {
		x.Rev = 0 // версия локальная
		out.Layouts = append(out.Layouts, x)
	}
	if l.Default != nil {
		out.Default = l.Default()
	}
	return json.Marshal(out)
}

func (l *Layouts) parse(raw json.RawMessage) ([]model.Layout, error) {
	var in layoutsData
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, errors.New("the layouts section is unreadable")
	}
	if len(in.Layouts) > maxItems {
		return nil, errors.New("too many layouts")
	}
	for i := range in.Layouts {
		in.Layouts[i].Rev = 0
	}
	return in.Layouts, nil
}

func sceneOwners(list []model.Layout) map[string]string {
	owner := map[string]string{}
	for _, x := range list {
		for _, sc := range x.Scenes {
			owner[sc] = x.ID
		}
	}
	return owner
}

func (l *Layouts) Plan(_ context.Context, raw json.RawMessage) ([]bundle.Item, error) {
	in, err := l.parse(raw)
	if err != nil {
		return nil, err
	}
	have, err := l.Store.ListLayouts()
	if err != nil {
		return nil, err
	}
	cur := map[string]model.Layout{}
	for _, h := range have {
		cur[h.ID] = h
	}
	owner := sceneOwners(have)
	items := make([]bundle.Item, 0, len(in))
	seen := map[string]bool{}
	for _, x := range in {
		it := bundle.Item{ID: x.ID, Name: x.Name}
		c := x
		msg := c.Validate()
		old, exists := cur[x.ID]
		switch {
		case seen[x.ID]:
			it.Status, it.Reason = bundle.StatusProblem, "The file lists this id twice."
		case msg != "":
			it.Status, it.Reason = bundle.StatusProblem, "Invalid layout: "+msg+"."
		case exists && sameLayout(old, c):
			it.Status = bundle.StatusSame
		case exists:
			it.Status = bundle.StatusReplace
			it.Reason, it.Warning = "Replaces the layout with this id (or import it as a copy).", true
		default:
			it.Status = bundle.StatusNew
		}
		if it.Status == bundle.StatusNew || it.Status == bundle.StatusReplace {
			var moved []string
			for _, sc := range c.Scenes {
				if o, taken := owner[sc]; taken && o != c.ID {
					moved = append(moved, sc)
				}
			}
			if len(moved) > 0 {
				sort.Strings(moved)
				it.Reason = strings.TrimSpace(it.Reason + " Takes over the OBS scene(s) " + strings.Join(moved, ", ") + " from another layout.")
				it.Warning = true
			}
		}
		seen[x.ID] = true
		items = append(items, it)
	}
	return items, nil
}

func sameLayout(a, b model.Layout) bool {
	a.Rev, b.Rev = 0, 0
	return reflect.DeepEqual(norm(a), norm(b))
}

func norm(x model.Layout) model.Layout {
	if x.Scenes == nil {
		x.Scenes = []string{}
	}
	if x.Objects == nil {
		x.Objects = []model.Object{}
	}
	return x
}

func (l *Layouts) Apply(_ context.Context, raw json.RawMessage, pick map[string]string) ([]bundle.Outcome, error) {
	in, err := l.parse(raw)
	if err != nil {
		return nil, err
	}
	have, err := l.Store.ListLayouts()
	if err != nil {
		return nil, err
	}
	used := map[string]bool{}
	for _, h := range have {
		used[h.ID] = true
	}
	var known map[string]bool
	if l.Sources != nil {
		srcs, err := l.Sources.ListSources()
		if err != nil {
			return nil, err
		}
		known = map[string]bool{}
		for _, s := range srcs {
			known[s.ID] = true
		}
	}
	var out []bundle.Outcome
	var saved []string
	for _, x := range in {
		mode, ok := pick[x.ID]
		if !ok {
			continue
		}
		o := bundle.Outcome{ID: x.ID}
		if msg := x.Validate(); msg != "" {
			o.Error = "Invalid layout: " + msg + "."
			out = append(out, o)
			continue
		}
		if mode == bundle.ModeCopy {
			x.ID = freeID(x.ID, used)
			x.Name = x.Name + " (imported)"
			x.Scenes = []string{} // сцена принадлежит одному layout: копия начинает без привязок
			objs := make([]model.Object, len(x.Objects))
			copy(objs, x.Objects)
			for i := range objs {
				objs[i].InstanceID = "" // копия не связана с виджетами других layout
			}
			x.Objects = objs
		}
		if known != nil {
			kept := x.Objects[:0:0]
			for _, ob := range x.Objects {
				if known[ob.SourceID] {
					kept = append(kept, ob)
				}
			}
			if dropped := len(x.Objects) - len(kept); dropped > 0 {
				o.Note = fmt.Sprintf("%d object(s) skipped: their sources do not exist here (import the sources too).", dropped)
			}
			x.Objects = kept
		}
		x.Rev = 0
		if _, err := l.Store.SaveLayout(x); err != nil {
			o.Error = err.Error()
		} else {
			o.OK = true
			used[x.ID] = true
			saved = append(saved, x.ID)
		}
		out = append(out, o)
	}
	if l.Changed != nil && len(saved) > 0 {
		l.Changed(saved)
	}
	return out, nil
}

// freeID подбирает id, которого ещё нет: id-2, id-3…
func freeID(id string, used map[string]bool) string {
	if !used[id] {
		return id
	}
	for i := 2; ; i++ {
		c := fmt.Sprintf("%s-%d", id, i)
		if len(c) > 64 {
			c = fmt.Sprintf("%s-%d", id[:64-len(fmt.Sprint(i))-1], i)
		}
		if !used[c] {
			return c
		}
	}
}
