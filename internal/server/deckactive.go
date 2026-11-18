package server

import (
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

// deckActiveTiles возвращает id плиток плагинов, у которых выполнено activeWhen.
func (s *Server) deckActiveTiles(l deck.Layout) []string {
	pl, ev := s.deckPlugins(), s.deckEventsSrc()
	w := &s.deckWatch
	w.mu.Lock()
	defer w.mu.Unlock()
	want := map[string]bool{}
	var on []string
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
				if !ok || a.ActiveWhen == nil {
					continue
				}
				key := in.Meta.ID + "\x00" + a.ActiveWhen.Topic
				want[key] = true
				if w.m == nil {
					w.m = map[string]*deckWatch{}
				}
				cur := w.m[key]
				if cur != nil && cur.drain() {
					cur, w.m[key] = nil, nil
				}
				if cur == nil {
					sub, err := ev.Subscribe(in.Meta.ID, a.ActiveWhen.Topic, 8)
					if err != nil {
						delete(w.m, key)
						continue
					}
					cur = &deckWatch{sub: sub}
					w.m[key] = cur
					cur.drain()
				}
				if cur.have && a.ActiveWhen.Match(cur.last) {
					on = append(on, t.ID)
				}
			}
		}
	}
	for k, cur := range w.m {
		if !want[k] {
			if cur != nil {
				cur.sub.Close()
			}
			delete(w.m, k)
		}
	}
	return on
}
