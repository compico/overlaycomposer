package server

import (
	"encoding/json"
	"errors"
	"sync"

	"github.com/compico/overlaycomposer/internal/events"
	"github.com/compico/overlaycomposer/internal/hub"
)

// Подписки страницы на события плагинов (ws plugin:sub / plugin:unsub, сервер шлёт plugin:event). Топик всегда принадлежит
// плагину, который назвала страница, и подписаться можно только на объявленный им топик; кто может обращаться к плагину,
// решает authorizePlugin, как для plugin:call.

// maxPluginSubsPerPeer — сколько подписок на плагины может держать одна страница (соединение).
const maxPluginSubsPerPeer = 64

type pluginSubReq struct {
	Type   string `json:"type"`
	ReqID  int    `json:"reqId"`
	Plugin string `json:"plugin"`
	Source string `json:"source"`
	Topic  string `json:"topic"`
}

type pluginSubRes struct {
	Type  string `json:"type"`
	ReqID int    `json:"reqId"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

type pluginEventMsg struct {
	Type     string          `json:"type"`
	Plugin   string          `json:"plugin"`
	Topic    string          `json:"topic"`
	Data     json.RawMessage `json:"data"`
	Seq      uint64          `json:"seq"`
	Retained bool            `json:"retained,omitempty"`
}

// peerSubs — подписки одного соединения.
type peerSubs struct {
	mu   sync.Mutex
	subs map[string]*events.Sub // ключ: плагин + "\x00" + топик
}

func (s *Server) peerSubsOf(p *hub.Peer) *peerSubs {
	s.evMu.Lock()
	defer s.evMu.Unlock()
	if s.evPeers == nil {
		s.evPeers = map[*hub.Peer]*peerSubs{}
	}
	ps := s.evPeers[p]
	if ps == nil {
		ps = &peerSubs{subs: map[string]*events.Sub{}}
		s.evPeers[p] = ps
	}
	return ps
}

// dropPeerSubs снимает все подписки закрывшегося соединения.
func (s *Server) dropPeerSubs(p *hub.Peer) {
	s.evMu.Lock()
	ps := s.evPeers[p]
	delete(s.evPeers, p)
	s.evMu.Unlock()
	if ps == nil {
		return
	}
	ps.mu.Lock()
	defer ps.mu.Unlock()
	for k, sub := range ps.subs {
		sub.Close()
		delete(ps.subs, k)
	}
}

func (s *Server) pluginAuthorize(plugin, source string) string {
	if s.pluginAuth != nil {
		return s.pluginAuth(plugin, source)
	}
	return s.authorizePlugin(plugin, source)
}

// pluginSubscription обрабатывает plugin:sub и plugin:unsub. Ответ приходит только на sub (plugin:sub:result).
func (s *Server) pluginSubscription(p *hub.Peer, raw []byte) {
	var m pluginSubReq
	if json.Unmarshal(raw, &m) != nil {
		return
	}
	key := m.Plugin + "\x00" + m.Topic
	if m.Type == "plugin:unsub" {
		ps := s.peerSubsOf(p)
		ps.mu.Lock()
		if sub := ps.subs[key]; sub != nil {
			sub.Close()
			delete(ps.subs, key)
		}
		ps.mu.Unlock()
		return
	}
	res := pluginSubRes{Type: "plugin:sub:result", ReqID: m.ReqID}
	fail := func(msg string) { res.Error = msg; p.Send(res) }
	if s.d.Events == nil {
		fail("not available")
		return
	}
	if msg := s.pluginAuthorize(m.Plugin, m.Source); msg != "" {
		fail(msg)
		return
	}
	ps := s.peerSubsOf(p)
	ps.mu.Lock()
	if ps.subs[key] != nil { // повторная подписка (например, после реконнекта страницы) — просто подтверждаем
		ps.mu.Unlock()
		res.OK = true
		p.Send(res)
		return
	}
	if len(ps.subs) >= maxPluginSubsPerPeer {
		ps.mu.Unlock()
		fail("too many subscriptions")
		return
	}
	sub, err := s.d.Events.Subscribe(m.Plugin, m.Topic, 0)
	if err != nil {
		ps.mu.Unlock()
		switch {
		case errors.Is(err, events.ErrNotDeclared):
			fail("unknown topic")
		default:
			fail("invalid topic")
		}
		return
	}
	ps.subs[key] = sub
	ps.mu.Unlock()
	res.OK = true
	p.Send(res)
	// События читаются из очереди подписки; медленная страница теряет их (Send не блокирует), на издателя это не влияет.
	go func() {
		for ev := range sub.C {
			p.Send(pluginEventMsg{Type: "plugin:event", Plugin: ev.Plugin, Topic: ev.Topic, Data: ev.Data, Seq: ev.Seq, Retained: ev.Retain})
		}
	}()
}
