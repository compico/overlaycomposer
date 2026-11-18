package server

import (
	"encoding/json"
	"sync"

	"github.com/compico/overlaycomposer/internal/deck"
	"github.com/compico/overlaycomposer/internal/events"
	"github.com/compico/overlaycomposer/internal/wasmplugins"
)

// Подсветка плиток плагинов: у действия плагина может быть activeWhen (топик его событий + условие). Сервер подписывается на нужные
// топики, помнит последнее событие и добавляет плитку в on общего состояния Deck, пока условие выполнено. Подписки создаются по
// требованию, лишние (плитку убрали из раскладки, плагин выгружен) закрываются при следующем расчёте состояния.

// deckEvents — то, что Deck нужно от брокера событий (в тестах подменяется).
type deckEvents interface {
	Subscribe(plugin, topic string, queue int) (*events.Sub, error)
}

type deckWatch struct {
	sub  *events.Sub
	last []byte // последнее значение события; nil — событий ещё не было
	have bool
}

type deckWatcher struct {
	mu sync.Mutex
	m  map[string]*deckWatch
}

func (s *Server) deckEventsSrc() deckEvents {
	if s.deckEventsOverride != nil {
		return s.deckEventsOverride
	}
	if s.d.Events == nil {
		return nil
	}
	return s.d.Events
}

// drain забирает накопившиеся события, оставляя последнее. Закрытый канал (плагин выгружен) означает «подписку нужно создать заново».
func (w *deckWatch) drain() (closed bool) {
	for {
		select {
		case ev, ok := <-w.sub.C:
			if !ok {
				return true
			}
			w.last, w.have = ev.Data, true
		default:
			return false
		}
	}
}

// deckTileState — то, что события плагинов дают состоянию панели: подсвеченные плитки и тексты плиток (id → строки).
type deckTileState struct {
	On   []string
	Text map[string][]string
}

// deckPluginTiles считает состояние плиток плагинов по их activeWhen и display.
func (s *Server) deckPluginTiles(l deck.Layout) deckTileState {
	pl, ev := s.deckPlugins(), s.deckEventsSrc()
	w := &s.deckWatch
	w.mu.Lock()
	defer w.mu.Unlock()
	want := map[string]bool{}
	var out deckTileState
	// value — последнее значение события топика плагина; подписка создаётся при первом обращении.
	value := func(plugin, topic string) (json.RawMessage, bool) {
		key := plugin + "\x00" + topic
		want[key] = true
		if w.m == nil {
			w.m = map[string]*deckWatch{}
		}
		cur := w.m[key]
		if cur != nil && cur.drain() {
			cur = nil
			delete(w.m, key)
		}
		if cur == nil {
			sub, err := ev.Subscribe(plugin, topic, 8)
			if err != nil {
				return nil, false
			}
			cur = &deckWatch{sub: sub}
			w.m[key] = cur
			cur.drain()
		}
		return cur.last, cur.have
	}
	if pl != nil && ev != nil {
		infos, _ := pl.List()
		byID := map[string]wasmplugins.Info{}
		for _, in := range infos {
			byID[in.Meta.ID] = in
		}
		for _, g := range l.Groups {
			for _, t := range g.Tiles {
				if t.Action.Type != deck.ActionPlugin {
					continue
				}
				in, ok := byID[t.Action.Plugin]
				if !ok || in.State != wasmplugins.StateRunning {
					continue
				}
				a, ok := in.Meta.FindAction(t.Action.PluginAction)
				if !ok {
					continue
				}
				if a.ActiveWhen != nil {
					if v, ok := value(in.Meta.ID, a.ActiveWhen.Topic); ok && a.ActiveWhen.Match(v) {
						out.On = append(out.On, t.ID)
					}
				}
				if a.Display != nil {
					if v, ok := value(in.Meta.ID, a.Display.Topic); ok {
						if lines := a.Display.Lines(v); len(lines) > 0 {
							if out.Text == nil {
								out.Text = map[string][]string{}
							}
							out.Text[t.ID] = lines
						}
					}
				}
			}
		}
	}
	for k, cur := range w.m {
		if !want[k] {
			cur.sub.Close()
			delete(w.m, k)
		}
	}
	return out
}
