package server

import (
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gofiber/contrib/v3/websocket"
	"github.com/gofiber/fiber/v3"

	"github.com/compico/overlaycomposer/internal/deck"
)

// Состояние панели Deck по WebSocket: GET /api/deck/ws. Сервер сам шлёт кадр {"t":"state",…} при подключении и при каждом
// изменении (сцена OBS, связь с OBS, ревизия раскладки), клиент ничего не отправляет. Без WebSocket (или если он упал) панель
// продолжает работать на опросе GET /api/deck/state — тот же состав полей.
//
//	сервер → {"t":"state","rev":3,"online":true,"scene":"Game","on":["game"]}
//
// Сервер раз в deckWSPing шлёт управляющий кадр ping (браузер отвечает сам); соединение без ответа дольше deckWSIdle закрывается.
const (
	deckWSPing     = 20 * time.Second
	deckWSIdle     = 70 * time.Second
	deckWSWrite    = 10 * time.Second
	deckWSMaxConns = 16
	deckTick       = 500 * time.Millisecond // как часто сервер сверяет состояние, пока кто-то подключён (OBS своих подписок для этого не даёт)
)

var deckWSConns atomic.Int32

// deckStateData — то, что устройство должно знать о состоянии. On — id плиток, которые подсвечены (активная сцена).
type deckStateData struct {
	T      string   `json:"t,omitempty"`
	Rev    int      `json:"rev"`
	Online bool     `json:"online"`
	Scene  string   `json:"scene"`
	On     []string `json:"on"`
	// Text — тексты плиток плагинов (id плитки → строки), например «сейчас играет».
	Text map[string][]string `json:"text,omitempty"`
}

// deckStateNow собирает текущее состояние. Плитки считаются на сервере, чтобы устройству не нужно было знать, что значит «подсвечена».
func (s *Server) deckStateNow() deckStateData {
	l, _ := s.deckLayout()
	st := deckStateData{Rev: l.Rev, On: []string{}}
	if o := s.deckObs(); o != nil {
		st.Online, st.Scene = o.Connected(), o.CurrentScene()
	}
	pt := s.deckPluginTiles(l)
	st.On, st.Text = append(st.On, pt.On...), pt.Text
	if st.Online {
		for _, g := range l.Groups {
			for _, t := range g.Tiles {
				if t.Action.Type == deck.ActionScene && t.Action.Scene != "" && t.Action.Scene == st.Scene {
					st.On = append(st.On, t.ID)
				}
			}
		}
	}
	return st
}

// deckHub рассылает состояние подписчикам. Пока подписчиков нет, ничего не делает; с первым подписчиком запускает сверку раз в deckTick.
type deckHub struct {
	compute func() deckStateData
	mu      sync.Mutex
	subs    map[chan []byte]struct{}
	last    []byte
	wake    chan struct{}
	stop    chan struct{}
}

func newDeckHub(compute func() deckStateData) *deckHub {
	return &deckHub{compute: compute, subs: map[chan []byte]struct{}{}, wake: make(chan struct{}, 1)}
}

func encodeDeckState(st deckStateData) []byte {
	st.T = "state"
	b, _ := json.Marshal(st)
	return b
}

// subscribe добавляет подписчика; его канал сразу получает текущее состояние. cancel снимает подписку (и останавливает сверку, если он последний).
func (h *deckHub) subscribe() (ch chan []byte, cancel func()) {
	ch = make(chan []byte, 4)
	cur := encodeDeckState(h.compute())
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.last = cur
	ch <- cur
	if len(h.subs) == 1 {
		h.stop = make(chan struct{})
		go h.loop(h.stop)
	}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if _, ok := h.subs[ch]; !ok {
			return
		}
		delete(h.subs, ch)
		if len(h.subs) == 0 && h.stop != nil {
			close(h.stop)
			h.stop = nil
		}
	}
}

// poke просит сверить состояние сейчас, не дожидаясь тика (сохранили раскладку — устройство перерисуется сразу).
func (h *deckHub) poke() {
	select {
	case h.wake <- struct{}{}:
	default:
	}
}

func (h *deckHub) loop(stop chan struct{}) {
	t := time.NewTicker(deckTick)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
		case <-h.wake:
		}
		h.check()
	}
}

// check шлёт подписчикам состояние, только если оно изменилось. Медленный подписчик теряет старые кадры, но не последний.
func (h *deckHub) check() {
	cur := encodeDeckState(h.compute())
	h.mu.Lock()
	defer h.mu.Unlock()
	if string(cur) == string(h.last) {
		return
	}
	h.last = cur
	for ch := range h.subs {
		for {
			select {
			case ch <- cur:
			default:
				select { // очередь полна: выбрасываем самый старый кадр и пробуем снова
				case <-ch:
					continue
				default:
				}
			}
			break
		}
	}
}

func (s *Server) deckHubOnce() *deckHub {
	s.deckHubMu.Lock()
	defer s.deckHubMu.Unlock()
	if s.deckHub == nil {
		s.deckHub = newDeckHub(s.deckStateNow)
	}
	return s.deckHub
}

// deckPoke — состояние могло измениться (раскладку сохранили или сбросили).
func (s *Server) deckPoke() { s.deckHubOnce().poke() }

func (s *Server) deckWSGuard(c fiber.Ctx) error {
	if !websocket.IsWebSocketUpgrade(c) {
		return fiber.ErrUpgradeRequired
	}
	if !pluginWSOriginOK(c.Get("Origin"), c.Get("Host")) {
		return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "cross-origin request"})
	}
	if deckWSConns.Load() >= deckWSMaxConns {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "too many connections"})
	}
	return c.Next()
}

func (s *Server) serveDeckWS(c *websocket.Conn) {
	if deckWSConns.Add(1) > deckWSMaxConns {
		deckWSConns.Add(-1)
		return
	}
	defer deckWSConns.Add(-1)
	ch, cancel := s.deckHubOnce().subscribe()
	defer cancel()

	c.SetReadLimit(256)
	_ = c.SetReadDeadline(time.Now().Add(deckWSIdle))
	c.SetPongHandler(func(string) error { return c.SetReadDeadline(time.Now().Add(deckWSIdle)) })
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { // единственный писатель
		defer wg.Done()
		t := time.NewTicker(deckWSPing)
		defer t.Stop()
		for {
			select {
			case b := <-ch:
				_ = c.SetWriteDeadline(time.Now().Add(deckWSWrite))
				if c.WriteMessage(websocket.TextMessage, b) != nil {
					_ = c.Close()
					return
				}
			case <-t.C:
				_ = c.SetWriteDeadline(time.Now().Add(deckWSWrite))
				if c.WriteMessage(websocket.PingMessage, nil) != nil {
					_ = c.Close()
					return
				}
			case <-done:
				return
			}
		}
	}()
	defer func() { close(done); wg.Wait() }()
	for { // клиент ничего не шлёт; чтение нужно, чтобы замечать закрытие и обрабатывать pong
		if _, _, err := c.ReadMessage(); err != nil {
			return
		}
		_ = c.SetReadDeadline(time.Now().Add(deckWSIdle))
	}
}
