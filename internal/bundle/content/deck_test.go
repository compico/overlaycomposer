package content

import (
	"context"
	"strings"
	"testing"

	"github.com/compico/overlaycomposer/internal/bundle"
	"github.com/compico/overlaycomposer/internal/deck"
)

type memDeck struct {
	l     deck.Layout
	saved bool
	rev   int
}

func (m *memDeck) Layout() (deck.Layout, bool) { return m.l, m.saved }
func (m *memDeck) Save(l deck.Layout) error {
	m.rev = m.l.Rev + 1
	l.Rev = m.rev
	m.l, m.saved = l, true
	return nil
}

func pluginTile(id string, x int, plugin string) deck.Tile {
	return deck.Tile{ID: id, X: x, Y: 0, W: 1, H: 1, Action: deck.Action{Type: deck.ActionPlugin, Plugin: plugin, PluginAction: "toggle"}}
}

func TestDeckExportPlanApply(t *testing.T) {
	src := &memDeck{l: deck.Default([]string{"Game", "Chat"}), saved: true}
	src.l.Rev = 5
	sec := &Deck{Store: src}
	raw, err := sec.Export(context.Background())
	if err != nil || strings.Contains(string(raw), `"rev":5`) {
		t.Fatalf("%s %v", raw, err)
	}

	// чистая машина: раскладки нет → new
	dst := &memDeck{l: deck.Default(nil)}
	dst.l.Rev = 9
	other := &Deck{Store: dst, Scenes: func() []string { return []string{"Game"} }}
	items, err := other.Plan(context.Background(), raw)
	if err != nil || len(items) != 1 || items[0].Status != bundle.StatusNew || !items[0].Warning || !strings.Contains(items[0].Reason, "Scenes not found in OBS: Chat") {
		t.Fatalf("%+v %v", items, err)
	}
	out, err := other.Apply(context.Background(), raw, map[string]string{DeckID: bundle.ModeReplace})
	if err != nil || len(out) != 1 || !out[0].OK || !strings.Contains(out[0].Note, "Chat") {
		t.Fatalf("%+v %v", out, err)
	}
	if !dst.saved || dst.rev != 10 || len(dst.l.Groups) != len(src.l.Groups) {
		t.Fatalf("%+v", dst)
	}
	// теперь то же самое → same, а изменённая — replace
	if items, _ = other.Plan(context.Background(), raw); items[0].Status != bundle.StatusSame {
		t.Fatalf("%+v", items)
	}
	dst.l.Groups[0].Name = "Other"
	if items, _ = other.Plan(context.Background(), raw); items[0].Status != bundle.StatusReplace || !strings.Contains(items[0].Reason, "Replaces") {
		t.Fatalf("%+v", items)
	}
}

func TestDeckUnsavedExportsNothing(t *testing.T) {
	sec := &Deck{Store: &memDeck{l: deck.Default([]string{"A"})}}
	raw, err := sec.Export(context.Background())
	if err != nil || strings.Contains(string(raw), "groups") {
		t.Fatalf("%s %v", raw, err)
	}
	if items, err := sec.Plan(context.Background(), raw); err != nil || len(items) != 0 {
		t.Fatalf("%+v %v", items, err)
	}
	if out, _ := sec.Apply(context.Background(), raw, map[string]string{DeckID: bundle.ModeReplace}); len(out) != 1 || out[0].OK {
		t.Fatalf("%+v", out)
	}
}

func TestDeckBadLayoutAndMissingPlugin(t *testing.T) {
	sec := &Deck{Store: &memDeck{l: deck.Default(nil)}, PluginKnown: func(id string) bool { return id == "music" }}
	items, err := sec.Plan(context.Background(), []byte(`{"layout":{"rev":0,"groups":[]}}`))
	if err != nil || len(items) != 1 || items[0].Status != bundle.StatusProblem || items[0].Reason == "" {
		t.Fatalf("%+v %v", items, err)
	}
	if items, _ = sec.Plan(context.Background(), []byte(`{"layout":{"rev":0,"groups":[],"extra":1}}`)); items[0].Status != bundle.StatusProblem {
		t.Fatalf("неизвестное поле: %+v", items)
	}
	l := deck.Layout{Groups: []deck.Group{{ID: "main", Tiles: []deck.Tile{pluginTile("t1", 0, "music"), pluginTile("t2", 1, "osu-tools")}}}}
	if err := l.Validate(); err != nil {
		t.Fatal(err)
	}
	raw, _ := (&Deck{Store: &memDeck{l: l, saved: true}}).Export(context.Background())
	items, _ = sec.Plan(context.Background(), raw)
	if items[0].Status != bundle.StatusNew || !strings.Contains(items[0].Reason, "Plugins not installed: osu-tools") || strings.Contains(items[0].Reason, "music") {
		t.Fatalf("%+v", items)
	}
	if out, _ := sec.Apply(context.Background(), raw, map[string]string{DeckID: bundle.ModeCopy}); out[0].OK {
		t.Fatalf("%+v", out)
	}
}
