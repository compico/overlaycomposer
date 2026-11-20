package content

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/compico/overlaycomposer/internal/bundle"
	"github.com/compico/overlaycomposer/internal/deck"
)

// DeckStore — то, что нужно секции Deck от сервера. Save ставит новый rev сам (устройства перерисуются) и оповещает панель.
type DeckStore interface {
	Layout() (l deck.Layout, saved bool) // saved=false — раскладка по умолчанию, своей нет
	Save(l deck.Layout) error
}

// DeckID — id единственного элемента секции.
const DeckID = "layout"

type deckData struct {
	Layout json.RawMessage `json:"layout"` // null — своей раскладки нет
}

// Deck — секция «deck»: раскладка панели Deck (плитки, группы, папки, плитки плагинов).
type Deck struct {
	Store DeckStore
	// Scenes — сцены OBS, известные сейчас (nil или пусто — не проверять); PluginKnown — есть ли такой плагин (nil — не проверять).
	// Нужны только для предупреждений: плитка с отсутствующей сценой или плагином остаётся в раскладке и просто вернёт ошибку при нажатии.
	Scenes      func() []string
	PluginKnown func(id string) bool
}

func (*Deck) Name() string { return "deck" }

func (d *Deck) Export(context.Context) (json.RawMessage, error) {
	l, saved := d.Store.Layout()
	if !saved {
		return json.Marshal(deckData{})
	}
	l.Rev = 0 // ревизия локальная
	raw, err := json.Marshal(l)
	if err != nil {
		return nil, err
	}
	return json.Marshal(deckData{Layout: raw})
}

func (d *Deck) parse(raw json.RawMessage) (*deck.Layout, error) {
	var in deckData
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, errors.New("the deck section is unreadable")
	}
	if len(in.Layout) == 0 || string(in.Layout) == "null" {
		return nil, nil
	}
	l, err := deck.Parse(in.Layout)
	if err != nil {
		return nil, err
	}
	l.Rev = 0
	return &l, nil
}

func (d *Deck) Plan(_ context.Context, raw json.RawMessage) ([]bundle.Item, error) {
	l, err := d.parse(raw)
	if err != nil {
		// негодная раскладка — это проблема элемента, а не секции: пользователь видит причину в модалке
		return []bundle.Item{{ID: DeckID, Name: "Deck panel", Status: bundle.StatusProblem, Reason: err.Error()}}, nil
	}
	if l == nil {
		return []bundle.Item{}, nil
	}
	tiles := 0
	for _, g := range l.Groups {
		tiles += len(g.Tiles)
	}
	it := bundle.Item{ID: DeckID, Name: fmt.Sprintf("Deck panel (%d groups, %d tiles)", len(l.Groups), tiles)}
	cur, saved := d.Store.Layout()
	cur.Rev = 0
	switch {
	case !saved:
		it.Status = bundle.StatusNew
	case canonEqual(cur, *l):
		it.Status = bundle.StatusSame
	default:
		it.Status, it.Reason, it.Warning = bundle.StatusReplace, "Replaces your current Deck layout.", true
	}
	if w := d.warnings(*l); w != "" {
		it.Reason, it.Warning = strings.TrimSpace(it.Reason+" "+w), true
	}
	return []bundle.Item{it}, nil
}

// warnings — что на панели сейчас не заработает: сцены, которых нет в OBS, и плагины, которых нет.
func (d *Deck) warnings(l deck.Layout) string {
	var scenes []string
	if d.Scenes != nil {
		scenes = d.Scenes()
	}
	have := map[string]bool{}
	for _, s := range scenes {
		have[s] = true
	}
	missScenes, missPlugins := map[string]bool{}, map[string]bool{}
	for _, g := range l.Groups {
		for _, t := range g.Tiles {
			a := t.Action
			if len(have) > 0 {
				switch a.Type {
				case deck.ActionScene:
					if !have[a.Scene] {
						missScenes[a.Scene] = true
					}
				case deck.ActionFolder:
					for _, s := range a.Scenes {
						if !have[s] {
							missScenes[s] = true
						}
					}
				}
			}
			if a.Type == deck.ActionPlugin && d.PluginKnown != nil && !d.PluginKnown(a.Plugin) {
				missPlugins[a.Plugin] = true
			}
		}
	}
	var parts []string
	if len(missScenes) > 0 {
		parts = append(parts, "Scenes not found in OBS: "+joinSorted(missScenes)+".")
	}
	if len(missPlugins) > 0 {
		parts = append(parts, "Plugins not installed: "+joinSorted(missPlugins)+" (their tiles will not work until you install them).")
	}
	return strings.Join(parts, " ")
}

func joinSorted(m map[string]bool) string {
	var l []string
	for k := range m {
		l = append(l, k)
	}
	sort.Strings(l)
	return strings.Join(l, ", ")
}

func (d *Deck) Apply(_ context.Context, raw json.RawMessage, pick map[string]string) ([]bundle.Outcome, error) {
	mode, ok := pick[DeckID]
	if !ok {
		return nil, nil
	}
	o := bundle.Outcome{ID: DeckID}
	l, err := d.parse(raw)
	switch {
	case mode != bundle.ModeReplace:
		o.Error = "the Deck layout is imported by replacing only"
	case err != nil:
		o.Error = err.Error()
	case l == nil:
		o.Error = "the file has no Deck layout"
	default:
		if err := d.Store.Save(*l); err != nil {
			o.Error = err.Error()
		} else {
			o.OK = true
			o.Note = d.warnings(*l)
		}
	}
	return []bundle.Outcome{o}, nil
}
