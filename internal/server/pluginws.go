package server

import (
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gofiber/contrib/v3/websocket"
	"github.com/gofiber/fiber/v3"

	"github.com/compico/overlaycomposer/internal/events"
)

// Внешний WebSocket плагина: GET /plugins/<id>/ws. Для клиентов вне страниц приложения — аддонов PulseSync, deck на планшете,
// сторонних скриптов. Только подписка на объявленные плагином топики: вызывать маршруты плагина по-прежнему можно обычным HTTP
// (/plugins/<id>/api/*), поэтому поверхность здесь минимальна.
//
// Протокол (текстовые JSON-кадры, дружит с Safari 9):
//
//	клиент → {"t":"sub","topic":"now"} | {"t":"unsub","topic":"now"} | {"t":"ping"}
//	сервер → {"t":"hello","plugin":"music"} при подключении
//	         {"t":"sub","topic":"now","ok":true} | {"t":"sub","topic":"now","ok":false,"error":"unknown topic"}
//	         {"t":"event","topic":"now","data":…,"seq":7,"retained":true}
//	         {"t":"pong"}
//
// Кроме того, сервер раз в pluginWSPing шлёт управляющий кадр ping (браузер отвечает сам); соединение, из которого ничего не
// приходит дольше pluginWSIdle, закрывается.
const (
	pluginWSPing      = 20 * time.Second
	pluginWSIdle      = 70 * time.Second
	pluginWSWrite     = 10 * time.Second
	pluginWSReadLimit = 4 << 10
	pluginWSMaxConns  = 64 // всего внешних соединений
	pluginWSMaxSubs   = 32 // подписок на соединение
	pluginWSMsgRate   = 20 // сообщений клиента в секунду (в среднем)
	pluginWSOutQueue  = 128
)

var pluginWSConns atomic.Int32

// pluginWSOriginOK: страницы чужих сайтов не должны читать события (там «что сейчас играет» и команды). Пропускаем запросы
// без Origin (не браузер), с Origin того же хоста и доверенный origin аддона. Origin «null» (sandbox-iframe) не пускаем:
// виджетам нужен мост oc.plugin.subscribe.
func pluginWSOriginOK(origin, host string) bool {
	if origin == "" || trustedPluginOrigin(origin) {
		return true
	}
	u, err := url.Parse(origin)
	return err == nil && u.Host != "" && strings.EqualFold(u.Host, host)
}

func (s *Server) pluginWSRoute() {
	s.app.Get("/plugins/:id/ws", s.pluginWSGuard, websocket.New(s.servePluginWS))
}

func (s *Server) pluginWSGuard(c fiber.Ctx) error {
	if !websocket.IsWebSocketUpgrade(c) {
		return fiber.ErrUpgradeRequired
	}
	if !pluginWSOriginOK(c.Get("Origin"), c.Get("Host")) {
		return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "cross-origin request"})
	}
	if s.d.Events == nil || s.d.Wasm == nil || !s.d.Wasm.Has(c.Params("id")) {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "not found"})
	}
	if pluginWSConns.Load() >= pluginWSMaxConns {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "too many connections"})
	}
	return c.Next()
}

type pluginWSIn struct {
	T     string `json:"t"`
	Topic string `json:"topic"`
}

type pluginWSOut struct {
	T        string          `json:"t"`
	Plugin   string          `json:"plugin,omitempty"`
	Topic    string          `json:"topic,omitempty"`
	OK       *bool           `json:"ok,omitempty"`
	Error    string          `json:"error,omitempty"`
	Data     json.RawMessage `json:"data,omitempty"`
	Seq      uint64          `json:"seq,omitempty"`
	Retained bool            `json:"retained,omitempty"`
}

func (s *Server) servePluginWS(c *websocket.Conn) {
	if pluginWSConns.Add(1) > pluginWSMaxConns {
		pluginWSConns.Add(-1)
		return
	}
	defer pluginWSConns.Add(-1)
	plugin := c.Params("id")

	c.SetReadLimit(pluginWSReadLimit)
	_ = c.SetReadDeadline(time.Now().Add(pluginWSIdle))
	c.SetPongHandler(func(string) error { return c.SetReadDeadline(time.Now().Add(pluginWSIdle)) })

	out := make(chan []byte, pluginWSOutQueue)
	done := make(chan struct{})
	send := func(m pluginWSOut) {
		b, err := json.Marshal(m)
		if err != nil {
			return
		}
		select {
		case out <- b:
		default: // медленный клиент теряет кадры, остальных это не касается
		}
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { // единственный писатель в соединение
		defer wg.Done()
		t := time.NewTicker(pluginWSPing)
		defer t.Stop()
		for {
			select {
			case b := <-out:
				_ = c.SetWriteDeadline(time.Now().Add(pluginWSWrite))
				if c.WriteMessage(websocket.TextMessage, b) != nil {
					_ = c.Close() // разбудит чтение
					return
				}
			case <-t.C:
				_ = c.SetWriteDeadline(time.Now().Add(pluginWSWrite))
				if c.WriteMessage(websocket.PingMessage, nil) != nil {
					_ = c.Close()
					return
				}
			case <-done:
				return
			}
		}
	}()

	subs := map[string]*events.Sub{}
	defer func() {
		close(done)
		for _, sub := range subs {
			sub.Close()
		}
		wg.Wait()
	}()

	send(pluginWSOut{T: "hello", Plugin: plugin})
	tokens, last := float64(pluginWSMsgRate*2), time.Now()
	for {
		mt, msg, err := c.ReadMessage()
		if err != nil {
			return
		}
		_ = c.SetReadDeadline(time.Now().Add(pluginWSIdle))
		if mt != websocket.TextMessage {
			continue
		}
		now := time.Now()
		tokens += now.Sub(last).Seconds() * pluginWSMsgRate
		if tokens > pluginWSMsgRate*2 {
			tokens = pluginWSMsgRate * 2
		}
		last = now
		if tokens < 1 {
			return // клиент завалил сервер сообщениями
		}
		tokens--
		var m pluginWSIn
		if json.Unmarshal(msg, &m) != nil {
			continue
		}
		switch m.T {
		case "ping":
			send(pluginWSOut{T: "pong"})
		case "unsub":
			if sub := subs[m.Topic]; sub != nil {
				sub.Close()
				delete(subs, m.Topic)
			}
		case "sub":
			ok, fail := true, false
			reply := func(good bool, e string) {
				r := pluginWSOut{T: "sub", Topic: m.Topic, Error: e}
				if good {
					r.OK = &ok
				} else {
					r.OK = &fail
				}
				send(r)
			}
			if subs[m.Topic] != nil {
				reply(true, "")
				continue
			}
			if len(subs) >= pluginWSMaxSubs {
				reply(false, "too many subscriptions")
				continue
			}
			sub, err := s.d.Events.Subscribe(plugin, m.Topic, 0)
			switch {
			case err == nil:
			case errors.Is(err, events.ErrNotDeclared):
				reply(false, "unknown topic")
				continue
			default:
				reply(false, "invalid topic")
				continue
			}
			subs[m.Topic] = sub
			reply(true, "")
			go func() {
				for ev := range sub.C {
					send(pluginWSOut{T: "event", Topic: ev.Topic, Data: ev.Data, Seq: ev.Seq, Retained: ev.Retain})
				}
			}()
		}
	}
}
