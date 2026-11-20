package content

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"testing"

	"github.com/compico/overlaycomposer/internal/bundle"
	"github.com/compico/overlaycomposer/internal/model"
)

type memSources struct{ m map[string]model.Source }

func (s *memSources) ListSources() ([]model.Source, error) {
	var out []model.Source
	for _, v := range s.m {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
func (s *memSources) SaveSource(x model.Source) error { s.m[x.ID] = x; return nil }

type memLayouts struct{ m map[string]model.Layout }

func (s *memLayouts) ListLayouts() ([]model.Layout, error) {
	var out []model.Layout
	for _, v := range s.m {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
func (s *memLayouts) SaveLayout(x model.Layout) (bool, error) {
	stolen := false
	for id, o := range s.m {
		if id == x.ID {
			continue
		}
		var keep []string
		for _, sc := range o.Scenes {
			if contains(x.Scenes, sc) {
				stolen = true
			} else {
				keep = append(keep, sc)
			}
		}
		o.Scenes = keep
		s.m[id] = o
	}
	s.m[x.ID] = x
	return stolen, nil
}

func contains(l []string, v string) bool {
	for _, x := range l {
		if x == v {
			return true
		}
	}
	return false
}

func cleaner(plugin, widget string, in map[string]any) (map[string]any, bool, error) {
	if plugin != "known" {
		return nil, false, nil
	}
	if _, bad := in["bad"]; bad {
		return nil, true, errors.New("bad is not allowed")
	}
	return in, true, nil
}

func mustRaw(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func statuses(items []bundle.Item) map[string]bundle.Item {
	out := map[string]bundle.Item{}
	for _, it := range items {
		out[it.ID] = it
	}
	return out
}

func layout(id string, scenes []string, srcs ...string) model.Layout {
	l := model.Layout{ID: id, Name: strings.ToUpper(id), Scenes: scenes, Canvas: model.Canvas{Width: 1920, Height: 1080}}
	for i, s := range srcs {
		l.Objects = append(l.Objects, model.Object{ID: id + s + string(rune('a'+i)), SourceID: s, InstanceID: "inst-" + s})
	}
	return l
}

func TestSourcesExportDropsGrantsAndPlansStatuses(t *testing.T) {
	st := &memSources{m: map[string]model.Source{
		"p": {ID: "p", Name: "P", Type: "plugin", Data: model.SourceData{Plugin: "known", Settings: map[string]any{"a": 1.0}, Granted: []string{"net"}}},
		"w": {ID: "w", Name: "W", Type: "iframe", Data: model.SourceData{URL: "https://example.com", Proxy: true}},
	}}
	sec := &Sources{Store: st, Settings: cleaner}
	raw, err := sec.Export(context.Background())
	if err != nil || strings.Contains(string(raw), `"granted"`) {
		t.Fatalf("%s %v", raw, err)
	}
	// тот же файл на «другом компьютере»
	dst := &memSources{m: map[string]model.Source{"w": st.m["w"]}}
	other := &Sources{Store: dst, Settings: cleaner}
	items, err := other.Plan(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	got := statuses(items)
	if got["p"].Status != bundle.StatusNew || got["w"].Status != bundle.StatusSame {
		t.Fatalf("%+v", got)
	}
}

func TestSourcesPlanProblemsAndWarnings(t *testing.T) {
	sec := &Sources{Store: &memSources{m: map[string]model.Source{}}, Settings: cleaner}
	raw := mustRaw(t, sourcesData{Sources: []model.Source{
		{ID: "ok", Name: "ok", Type: "iframe", Data: model.SourceData{URL: "https://a.example"}},
		{ID: "js", Name: "js", Type: "iframe", Data: model.SourceData{URL: "javascript:alert(1)"}},
		{ID: "px", Name: "px", Type: "iframe", Data: model.SourceData{URL: "/local", Proxy: true}},
		{ID: "bad id!", Name: "x", Type: "iframe", Data: model.SourceData{URL: "https://a.example"}},
		{ID: "stg", Name: "stg", Type: "plugin", Data: model.SourceData{Plugin: "known", Settings: map[string]any{"bad": 1.0}}},
		{ID: "unk", Name: "unk", Type: "plugin", Data: model.SourceData{Plugin: "later", Settings: map[string]any{"a": "x", "o": map[string]any{"x": 1.0}}}},
		{ID: "ok", Name: "dup", Type: "iframe", Data: model.SourceData{URL: "https://b.example"}},
	}})
	items, err := sec.Plan(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 7 {
		t.Fatalf("%d", len(items))
	}
	if items[0].Status != bundle.StatusNew || items[1].Status != bundle.StatusProblem || items[2].Status != bundle.StatusProblem ||
		items[3].Status != bundle.StatusProblem || items[4].Status != bundle.StatusProblem || !strings.Contains(items[4].Reason, "bad is not allowed") {
		t.Fatalf("%+v", items)
	}
	if items[5].Status != bundle.StatusNew || !items[5].Warning || !strings.Contains(items[5].Reason, "not installed yet") {
		t.Fatalf("%+v", items[5])
	}
	if items[6].Status != bundle.StatusProblem || !strings.Contains(items[6].Reason, "twice") {
		t.Fatalf("%+v", items[6])
	}
}

func TestSourcesApplyKeepsLocalGrantsAndDropsComplexSettings(t *testing.T) {
	st := &memSources{m: map[string]model.Source{
		"p": {ID: "p", Name: "old", Type: "plugin", Data: model.SourceData{Plugin: "known", Granted: []string{"net"}}},
		"q": {ID: "q", Name: "old", Type: "plugin", Data: model.SourceData{Plugin: "known", Granted: []string{"net"}}},
	}}
	changed := 0
	sec := &Sources{Store: st, Settings: cleaner, Changed: func() { changed++ }}
	raw := mustRaw(t, sourcesData{Sources: []model.Source{
		{ID: "p", Name: "new", Type: "plugin", Data: model.SourceData{Plugin: "known", Granted: []string{"all"}}},                                  // тот же плагин: локальные права остаются, из файла не берутся
		{ID: "q", Name: "new", Type: "plugin", Data: model.SourceData{Plugin: "other", Settings: map[string]any{"o": map[string]any{}, "s": "x"}}}, // другой плагин: прав нет
		{ID: "n", Name: "n", Type: "iframe", Data: model.SourceData{URL: "https://a.example"}},
	}})
	out, err := sec.Apply(context.Background(), raw, map[string]string{"p": bundle.ModeReplace, "q": bundle.ModeReplace, "n": bundle.ModeReplace})
	if err != nil || len(out) != 3 || changed != 1 {
		t.Fatalf("%+v %v %d", out, err, changed)
	}
	for _, o := range out {
		if !o.OK {
			t.Fatalf("%+v", o)
		}
	}
	if st.m["p"].Name != "new" || len(st.m["p"].Data.Granted) != 1 || st.m["p"].Data.Granted[0] != "net" {
		t.Fatalf("%+v", st.m["p"])
	}
	if st.m["q"].Data.Granted != nil || len(st.m["q"].Data.Settings) != 1 || st.m["q"].Data.Settings["s"] != "x" {
		t.Fatalf("%+v", st.m["q"])
	}
	// копия для источников не поддерживается
	out, _ = sec.Apply(context.Background(), raw, map[string]string{"n": bundle.ModeCopy})
	if len(out) != 1 || out[0].OK || out[0].Error == "" {
		t.Fatalf("%+v", out)
	}
}

func TestLayoutsPlan(t *testing.T) {
	have := &memLayouts{m: map[string]model.Layout{
		"a": layout("a", []string{"Game"}, "s1"),
		"b": layout("b", []string{"Chat"}),
	}}
	sec := &Layouts{Store: have}
	changedB := layout("b", nil, "s2")
	incoming := mustRaw(t, layoutsData{Layouts: []model.Layout{
		layout("a", []string{"Game"}, "s1"), // то же
		changedB,                            // заменит
		layout("c", []string{"Game"}, "s1"), // новый, заберёт сцену
		{ID: "d", Name: "", Canvas: model.Canvas{Width: 1, Height: 1}}, // невалидный
	}})
	items, err := sec.Plan(context.Background(), incoming)
	if err != nil {
		t.Fatal(err)
	}
	got := statuses(items)
	if got["a"].Status != bundle.StatusSame || got["b"].Status != bundle.StatusReplace || got["c"].Status != bundle.StatusNew || got["d"].Status != bundle.StatusProblem {
		t.Fatalf("%+v", got)
	}
	if !got["c"].Warning || !strings.Contains(got["c"].Reason, "Game") {
		t.Fatalf("%+v", got["c"])
	}
}

func TestLayoutsApplyReplaceCopyAndMissingSources(t *testing.T) {
	have := &memLayouts{m: map[string]model.Layout{"a": layout("a", []string{"Game"}, "s1")}}
	srcs := &memSources{m: map[string]model.Source{"s1": {ID: "s1"}}}
	changed := 0
	sec := &Layouts{Store: have, Sources: srcs, Changed: func(ids []string) { changed += len(ids) }}
	in := layout("a", []string{"Game", "Main"}, "s1", "gone")
	in.Name = "A new"
	raw := mustRaw(t, layoutsData{Layouts: []model.Layout{in, layout("n", []string{"X"}, "s1")}})

	out, err := sec.Apply(context.Background(), raw, map[string]string{"a": bundle.ModeReplace, "n": bundle.ModeCopy})
	if err != nil || len(out) != 2 || changed != 2 {
		t.Fatalf("%+v %v %d", out, err, changed)
	}
	a := have.m["a"]
	if a.Name != "A new" || len(a.Objects) != 1 || a.Objects[0].SourceID != "s1" || len(a.Scenes) != 2 {
		t.Fatalf("%+v", a)
	}
	if !strings.Contains(out[0].Note, "1 object") {
		t.Fatalf("%+v", out[0])
	}
	n := have.m["n"] // копия: нового id нет в базе, но он свободен — id сохраняется, сцены пусты, связи сняты
	if n.Name != "N (imported)" || len(n.Scenes) != 0 || n.Objects[0].InstanceID != "" {
		t.Fatalf("%+v", n)
	}
	// копия с занятым id получает -2
	out, _ = sec.Apply(context.Background(), mustRaw(t, layoutsData{Layouts: []model.Layout{layout("a", nil, "s1")}}), map[string]string{"a": bundle.ModeCopy})
	if len(out) != 1 || !out[0].OK || have.m["a-2"].ID != "a-2" {
		t.Fatalf("%+v %v", out, have.m)
	}
}

func TestLayoutsExportRoundTripThroughBundle(t *testing.T) {
	a := layout("a", []string{"Game"}, "s1")
	a.Rev = 7
	srcLayouts := &memLayouts{m: map[string]model.Layout{"a": a}}
	srcSources := &memSources{m: map[string]model.Source{"s1": {ID: "s1", Name: "S", Type: "iframe", Data: model.SourceData{URL: "https://a.example"}}}}
	mk := func(l *memLayouts, s *memSources) *bundle.Service {
		return bundle.New("t", &Sources{Store: s}, &Layouts{Store: l, Sources: s, Default: func() string { return "a" }})
	}
	data, _ := json.Marshal(mustExport(t, mk(srcLayouts, srcSources)))
	if strings.Contains(string(data), `"rev":7`) {
		t.Fatal("rev не должен попадать в файл")
	}
	f, err := bundle.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	dstL, dstS := &memLayouts{m: map[string]model.Layout{}}, &memSources{m: map[string]model.Source{}}
	svc := mk(dstL, dstS)
	rep, err := svc.Apply(context.Background(), f, bundle.Selection{"sources": {"s1": bundle.ModeReplace}, "layouts": {"a": bundle.ModeReplace}})
	if err != nil || len(rep) != 2 || rep[0].Name != "sources" {
		t.Fatalf("%+v %v", rep, err)
	}
	if got := dstL.m["a"]; len(got.Objects) != 1 || got.Scenes[0] != "Game" {
		t.Fatalf("%+v", got)
	}
}

func mustExport(t *testing.T, s *bundle.Service) bundle.File {
	t.Helper()
	f, err := s.Export(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return f
}
