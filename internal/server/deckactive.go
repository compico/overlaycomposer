package server

import (
	"encoding/json"
	"sync"

	"github.com/compico/overlaycomposer/internal/deck"
	"github.com/compico/overlaycomposer/internal/events"
	"github.com/compico/overlaycomposer/internal/wasmplugins"
)

// Плитки плагинов на панели Deck. Действие плагина может объявить activeWhen (подсветка) и display (текст на плитке) по событиям
// его топика. Сервер подписывается на нужные топики, помнит последнее событие и отдаёт результат в общем состоянии Deck.
// Подписки создаются по требованию, лишние (плитку убрали из раскладки, плагин выгружен) закрываются при следующем расчёте.
//
// Сущности: deckWatcher хранит подписки, deckSession — один расчёт состояния поверх него, deckPluginIndex находит действие
// плитки среди запущенных плагинов, deckTileState собирает результат.

// deckEvents — то, что Deck нужно от брокера событий (в тестах подменяется).
type deckEvents interface {
	Subscribe(plugin, topic string, queue int) (*events.Sub, error)
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

// deckWatch — подписка на один топик плагина и его последнее значение.
type deckWatch struct {
	sub  *events.Sub
	last json.RawMessage // последнее значение события
	have bool            // событие уже было
}

// drain забирает накопившиеся события, оставляя последнее. Закрытый канал (плагин выгружен) значит «подписку нужно создать заново».
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

// deckWatcher — подписки панели на топики плагинов; ключ — плагин и топик.
type deckWatcher struct {
	mu sync.Mutex
	m  map[string]*deckWatch
}

// begin открывает расчёт состояния; mu удерживается до deckSession.end.
func (w *deckWatcher) begin(ev deckEvents) *deckSession {
	w.mu.Lock()
	if w.m == nil {
		w.m = map[string]*deckWatch{}
	}
	return &deckSession{w: w, ev: ev, used: map[string]bool{}}
}

// deckSession — один расчёт состояния: какие топики понадобились, те подписки и остаются.
type deckSession struct {
	w    *deckWatcher
	ev   deckEvents
	used map[string]bool
}

// value — последнее значение события топика; подписка создаётся при первом обращении.
func (ss *deckSession) value(plugin, topic string) (json.RawMessage, bool) {
	key := plugin + "\x00" + topic
	ss.used[key] = true
	cur := ss.w.m[key]
	if cur != nil && cur.drain() {
		delete(ss.w.m, key)
		cur = nil
	}
	if cur == nil {
		sub, err := ss.ev.Subscribe(plugin, topic, 8)
		if err != nil {
			return nil, false
		}
		cur = &deckWatch{sub: sub}
		ss.w.m[key] = cur
		cur.drain() // retained-значение приходит сразу
	}
	return cur.last, cur.have
}

// end закрывает подписки, которые этот расчёт не запрашивал, и отпускает watcher.
func (ss *deckSession) end() {
	for k, cur := range ss.w.m {
		if !ss.used[k] {
			cur.sub.Close()
			delete(ss.w.m, k)
		}
	}
	ss.w.mu.Unlock()
}

// deckPluginIndex — запущенные плагины по id.
type deckPluginIndex map[string]wasmplugins.Info

func newDeckPluginIndex(pl deckPlugins) deckPluginIndex {
	infos, _ := pl.List()
	x := deckPluginIndex{}
	for _, in := range infos {
		if in.State == wasmplugins.StateRunning {
			x[in.Meta.ID] = in
		}
	}
	return x
}

// action — объявленное действие, на которое указывает плитка; ok=false, если плитка не плагина, плагин не запущен или действия нет.
func (x deckPluginIndex) action(t deck.Tile) (a wasmplugins.Action, ok bool) {
	if t.Action.Type != deck.ActionPlugin {
		return a, false
	}
	in, found := x[t.Action.Plugin]
	if !found {
		return a, false
	}
	return in.Meta.FindAction(t.Action.PluginAction)
}

// deckTileState — то, что события плагинов дают состоянию панели: подсвеченные плитки и тексты плиток (id → строки).
type deckTileState struct {
	On   []string
	Text map[string][]string
}

// update учитывает плитку: подсветка по activeWhen и текст по display.
func (st *deckTileState) update(t deck.Tile, a wasmplugins.Action, ss *deckSession) {
	if w := a.ActiveWhen; w != nil {
		if v, ok := ss.value(t.Action.Plugin, w.Topic); ok && w.Match(v) {
			st.On = append(st.On, t.ID)
		}
	}
	if d := a.Display; d != nil {
		if v, ok := ss.value(t.Action.Plugin, d.Topic); ok {
			st.setText(t.ID, d.Lines(v))
		}
	}
}

func (st *deckTileState) setText(id string, lines []string) {
	if len(lines) == 0 {
		return
	}
	if st.Text == nil {
		st.Text = map[string][]string{}
	}
	st.Text[id] = lines
}

// deckPluginTiles считает состояние плиток плагинов раскладки.
func (s *Server) deckPluginTiles(l deck.Layout) (st deckTileState) {
	pl, ev := s.deckPlugins(), s.deckEventsSrc()
	if pl == nil || ev == nil {
		s.deckWatch.begin(nil).end() // плагинов нет: старые подписки закрываются
		return st
	}
	ss := s.deckWatch.begin(ev)
	defer ss.end()
	plugins := newDeckPluginIndex(pl)
	for _, g := range l.Groups {
		for _, t := range g.Tiles {
			if a, ok := plugins.action(t); ok {
				st.update(t, a, ss)
			}
		}
	}
	return st
}
