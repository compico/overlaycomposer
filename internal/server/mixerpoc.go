package server

import (
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/gofiber/contrib/v3/websocket"
	"github.com/gofiber/fiber/v3"

	"github.com/compico/overlaycomposer/internal/deck"
	"github.com/compico/overlaycomposer/internal/obs"
)

// PoC микшера (#234): страница /mixer-poc и её WebSocket. Только с этого компьютера: это проверка идеи, не готовая функция.
// Сервер подписывается на уровни OBS, пока страница открыта, и шлёт кадры {"online":true,"inputs":[…]}; команды страницы
// ({"t":"toggle","name":…}, {"t":"mute","name":…,"muted":bool}, {"t":"vol","name":…,"db":число, не выше 0}) выполняются только для активных источников.
const mixerPocPing = 20 * time.Second

type mixerPocFrame struct {
	Online bool             `json:"online"`
	Inputs []obs.MixerInput `json:"inputs"`
}

func (s *Server) mixerPocRoutes() {
	s.app.Get("/mixer-poc", s.localOnly, func(c fiber.Ctx) error {
		c.Set("Cache-Control", "no-store")
		c.Set("Content-Security-Policy", deck.CSPFor(c.Hostname()))
		c.Set("X-Content-Type-Options", "nosniff")
		return s.htmlString(c, mixerPocPage)
	})
	s.app.Get("/api/mixer-poc/ws", s.localOnly, func(c fiber.Ctx) error {
		if !websocket.IsWebSocketUpgrade(c) {
			return fiber.ErrUpgradeRequired
		}
		if !pluginWSOriginOK(c.Get("Origin"), c.Get("Host")) {
			return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "cross-origin request"})
		}
		return c.Next()
	}, websocket.New(s.serveMixerPoc))
}

func (s *Server) serveMixerPoc(c *websocket.Conn) {
	o := s.d.Obs
	if o == nil {
		return
	}
	c.SetReadLimit(512)
	ch := make(chan []obs.MixerInput, 1)
	push := func(in []obs.MixerInput) { // не блокирует OBS-клиент: медленной странице достаётся только последний кадр
		select {
		case ch <- in:
		default:
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- in:
			default:
			}
		}
	}
	stop := o.WatchMixer(push)
	defer stop()

	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { // единственный писатель
		defer wg.Done()
		idle := time.NewTicker(time.Second) // без событий уровней страница всё равно узнаёт, есть ли связь с OBS
		defer idle.Stop()
		ping := time.NewTicker(mixerPocPing)
		defer ping.Stop()
		last := []obs.MixerInput{}
		send := func() bool {
			b, err := json.Marshal(mixerPocFrame{Online: o.Connected(), Inputs: last})
			if err != nil {
				return true
			}
			_ = c.SetWriteDeadline(time.Now().Add(deckWSWrite))
			if c.WriteMessage(websocket.TextMessage, b) != nil {
				_ = c.Close()
				return false
			}
			return true
		}
		for {
			select {
			case in := <-ch:
				last = in
				if !send() {
					return
				}
				idle.Reset(time.Second)
			case <-idle.C:
				if !send() {
					return
				}
			case <-ping.C:
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

	for {
		_, raw, err := c.ReadMessage()
		if err != nil {
			return
		}
		var m struct {
			T     string  `json:"t"`
			Name  string  `json:"name"`
			Muted bool    `json:"muted"`
			DB    float64 `json:"db"`
		}
		if json.Unmarshal(raw, &m) != nil {
			continue
		}
		var cerr error
		switch m.T {
		case "toggle":
			cerr = o.ToggleInputMute(m.Name)
		case "mute":
			cerr = o.SetInputMute(m.Name, m.Muted)
		case "vol":
			cerr = o.SetInputVolumeDB(m.Name, m.DB)
		}
		if cerr != nil {
			slog.Warn("mixer-poc: команда не выполнена", "type", m.T, "err", cerr)
		}
	}
}
