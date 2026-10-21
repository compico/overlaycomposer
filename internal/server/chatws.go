package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"

	"github.com/compico/overlaycomposer/internal/chat"
	"github.com/compico/overlaycomposer/internal/hub"
)

// originGuard защищает от запросов с чужих страниц: любой сайт (и любой плагин в sandbox-iframe с origin "null") может
// отправить POST или открыть WebSocket на localhost. Браузер всегда прикладывает Origin к таким запросам —
// если он не совпадает с Host, отказываем. Обычное чтение (GET) не затрагиваем: ответ чужая страница всё равно не прочитает.
func (s *Server) originGuard(c fiber.Ctx) error {
	switch c.Method() {
	case fiber.MethodGet, fiber.MethodHead, fiber.MethodOptions:
		if c.Path() != "/ws" {
			return c.Next()
		}
	}
	o := c.Get("Origin")
	if o == "" {
		return c.Next()
	}
	u, err := url.Parse(o)
	if err != nil || !strings.EqualFold(u.Host, c.Get("Host")) {
		return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "cross-origin request"})
	}
	return c.Next()
}

// ---- чат для плагинов по общему WebSocket ----
//
// Страница оверлея (а не сам плагин — он в sandbox) подписывается на каналы и отправляет сообщения;
// события приходят той же WebSocket-связью, что и остальные сообщения приложения.

type chatReq struct {
	Type    string `json:"type"`
	ReqID   int    `json:"reqId"`
	Channel string `json:"channel"`
	Text    string `json:"text"`
	As      string `json:"as"`
	ReplyTo string `json:"replyTo"`
}

type chatEventMsg struct {
	Type string `json:"type"`
	chat.Event
}

type chatResult struct {
	Type    string `json:"type"`
	ReqID   int    `json:"reqId"`
	OK      bool   `json:"ok"`
	Channel string `json:"channel,omitempty"`
	Error   string `json:"error,omitempty"`
}

func (s *Server) chatWS() {
	h := s.d.Hub
	h.Handle("chat:", func(p *hub.Peer, raw []byte) {
		var m chatReq
		if json.Unmarshal(raw, &m) != nil {
			return
		}
		switch m.Type {
		case "chat:sub":
			ch, err := s.d.Chat.Subscribe(p, m.Channel, func(ev chat.Event) {
				p.Send(chatEventMsg{Type: "chat:event", Event: ev})
			})
			res := chatResult{Type: "chat:sub:result", ReqID: m.ReqID, OK: err == nil, Channel: ch}
			if err != nil {
				res.Error = err.Error()
			}
			p.Send(res)
		case "chat:unsub":
			s.d.Chat.Unsubscribe(p, m.Channel)
		case "chat:say":
			// Отправка идёт по сети (подключение к Twitch до 15 с) — не блокируем чтение сообщений клиента.
			req := chat.SayRequest{Channel: m.Channel, Text: m.Text, As: m.As, ReplyTo: m.ReplyTo}
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				err := s.d.Chat.Say(ctx, req)
				res := chatResult{Type: "chat:say:result", ReqID: m.ReqID, OK: err == nil}
				if err != nil {
					res.Error = err.Error()
					slog.Warn("chat: отправка не удалась", "err", err)
				}
				p.Send(res)
			}()
		}
	})
	h.OnPeerGone(func(p *hub.Peer) { s.d.Chat.UnsubscribeAll(p) })
}
