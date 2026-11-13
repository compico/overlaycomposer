package server

import (
	"errors"
	"html"
	"log/slog"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v3"

	"github.com/compico/overlaycomposer/internal/notify"
	"github.com/compico/overlaycomposer/internal/twitch"
)

// Всё, что связано с аккаунтами Twitch, доступно только с этого компьютера: сопряжённое по сети устройство
// не должно ни подключать чужие аккаунты, ни менять Client Secret.
func (s *Server) twitchRoutes() {
	app := s.app
	app.Get("/api/twitch", s.localOnly, s.twitchStatus)
	app.Put("/api/twitch/app", s.localOnly, s.appGuard, s.twitchSetApp)
	app.Post("/api/twitch/connect/:role", s.localOnly, s.appGuard, s.twitchConnect)
	app.Delete("/api/twitch/accounts/:role", s.localOnly, s.appGuard, s.twitchDisconnect)
	app.Get("/auth/twitch/callback", s.localOnly, s.twitchCallback)
	app.Get("/api/twitch/device/:flow", s.localOnly, s.twitchDeviceStatus)
	app.Delete("/api/twitch/app", s.localOnly, s.appGuard, s.twitchResetApp)
	if s.d.Twitch != nil { // подключение по коду завершается в фоне: чат и уведомления обновляем по готовности
		s.d.Twitch.OnConnect(func(a twitch.Account) {
			slog.Info("twitch: аккаунт подключён по коду", "role", a.Role, "login", a.Login)
			if s.d.Chat != nil {
				s.d.Chat.Reload()
			}
			s.SyncTwitchNotifications()
		})
	}
}

// twitchRedirectURI — Twitch разрешает http только для localhost, поэтому имя именно localhost (а не 127.0.0.1).
func (s *Server) twitchRedirectURI() string {
	return "http://localhost:" + strconv.Itoa(s.d.Running.Port) + "/auth/twitch/callback"
}

// SyncTwitchNotifications приводит уведомления «переподключите аккаунт» в соответствие с текущим состоянием аккаунтов.
// Вызывается при запуске, периодически и после любых изменений аккаунтов.
func (s *Server) SyncTwitchNotifications() {
	if s.d.Notify == nil || s.d.Twitch == nil {
		return
	}
	accs, err := s.d.Twitch.Accounts()
	if err != nil {
		slog.Warn("twitch: не удалось прочитать аккаунты для уведомлений", "err", err)
		return
	}
	s.d.Notify.Sync(notify.KindTwitchReconnect, twitch.ReconnectItems(accs))
}

func (s *Server) twitchStatus(c fiber.Ctx) error {
	id, hasSecret, err := s.d.Twitch.AppInfo()
	if err != nil {
		return serverErr(c, err)
	}
	accs, err := s.d.Twitch.Accounts()
	if err != nil {
		return serverErr(c, err)
	}
	s.SyncTwitchNotifications()
	if accs == nil {
		accs = []twitch.Account{}
	}
	mode, err := s.d.Twitch.Mode()
	if err != nil {
		return serverErr(c, err)
	}
	return c.JSON(fiber.Map{
		"mode":        mode, // device — встроенное/Public-приложение (подключение по коду), code — своё приложение с секретом, "" — не настроено
		"builtIn":     s.d.Twitch.BuiltIn(),
		"clientId":    id, // только своё приложение
		"hasSecret":   hasSecret,
		"redirectUri": s.twitchRedirectURI(),
		"accounts":    accs,
	})
}

func (s *Server) twitchSetApp(c fiber.Ctx) error {
	var b struct {
		ClientID     string `json:"clientId"`
		ClientSecret string `json:"clientSecret"`
	}
	if !body(c, &b) {
		return bad(c, "invalid json")
	}
	if err := s.d.Twitch.SetApp(b.ClientID, b.ClientSecret); err != nil {
		return bad(c, err.Error())
	}
	return s.twitchStatus(c)
}

func (s *Server) twitchConnect(c fiber.Ctx) error {
	role := strings.Clone(c.Params("role")) // Fiber переиспользует буферы запроса
	mode, err := s.d.Twitch.Mode()
	if err != nil {
		return serverErr(c, err)
	}
	if mode == "device" {
		d, err := s.d.Twitch.StartDevice(c.Context(), role)
		switch {
		case err == nil:
			return c.JSON(fiber.Map{"mode": "device", "url": d.VerificationURI, "userCode": d.UserCode, "flow": d.Flow, "expiresIn": d.ExpiresIn})
		case errors.Is(err, twitch.ErrBadRole), errors.Is(err, twitch.ErrNotConfigured):
			return bad(c, err.Error())
		}
		return serverErr(c, err)
	}
	u, err := s.d.Twitch.AuthURL(role, s.twitchRedirectURI())
	switch {
	case err == nil:
		return c.JSON(fiber.Map{"mode": "code", "url": u})
	case errors.Is(err, twitch.ErrBadRole), errors.Is(err, twitch.ErrNotConfigured):
		return bad(c, err.Error())
	}
	return serverErr(c, err)
}

// twitchDeviceStatus — как идёт подключение по коду (клиент опрашивает, пока статус pending).
func (s *Server) twitchDeviceStatus(c fiber.Ctx) error {
	st, ok := s.d.Twitch.DeviceStatus(strings.Clone(c.Params("flow")))
	if !ok {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "unknown or cancelled connection"})
	}
	return c.JSON(st)
}

// twitchResetApp забывает своё приложение (Client ID / Secret) и возвращает встроенное; аккаунты при этом отключаются.
func (s *Server) twitchResetApp(c fiber.Ctx) error {
	if err := s.d.Twitch.ResetApp(); err != nil {
		return serverErr(c, err)
	}
	if s.d.Chat != nil {
		s.d.Chat.Reload()
	}
	s.SyncTwitchNotifications()
	return s.twitchStatus(c)
}

func (s *Server) twitchDisconnect(c fiber.Ctx) error {
	err := s.d.Twitch.Disconnect(c.Context(), c.Params("role"))
	switch {
	case err == nil:
		s.d.Chat.Reload()
		s.SyncTwitchNotifications()
		return c.JSON(fiber.Map{"ok": true})
	case errors.Is(err, twitch.ErrBadRole):
		return bad(c, err.Error())
	}
	return serverErr(c, err)
}

// twitchCallback — сюда Twitch возвращает браузер после согласия пользователя.
func (s *Server) twitchCallback(c fiber.Ctx) error {
	if e := c.Query("error"); e != "" {
		return s.twitchResult(c, fiber.StatusBadRequest, "Twitch denied access", e+": "+c.Query("error_description"))
	}
	// Fiber переиспользует буферы запроса — значения копируем.
	state, code := strings.Clone(c.Query("state")), strings.Clone(c.Query("code"))
	acc, err := s.d.Twitch.Callback(c.Context(), state, code, s.twitchRedirectURI())
	if err != nil {
		slog.Warn("twitch: подключение не удалось", "err", err)
		return s.twitchResult(c, fiber.StatusBadRequest, "Could not connect", err.Error())
	}
	slog.Info("twitch: аккаунт подключён", "role", acc.Role, "login", acc.Login)
	s.d.Chat.Reload() // соединения для отправки пересоздадутся уже с новым аккаунтом
	s.SyncTwitchNotifications()
	return s.twitchResult(c, fiber.StatusOK, "Connected as "+acc.Login, "Role: "+acc.Role+". You can close this tab and return to Overlay Composer.")
}

func (s *Server) twitchResult(c fiber.Ctx, status int, title, msg string) error {
	c.Status(status)
	c.Set("Cache-Control", "no-store")
	return s.htmlString(c, `<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Overlay Composer — Twitch</title>
<link rel="icon" type="image/svg+xml" href="data:image/svg+xml,%3Csvg xmlns=%27http://www.w3.org/2000/svg%27 viewBox=%270 0 256 256%27%3E%3Crect x=%276%27 y=%276%27 width=%27244%27 height=%27244%27 rx=%2756%27 fill=%27%231a1a2e%27/%3E%3Cg stroke=%27%23fff%27 stroke-width=%274%27 stroke-linejoin=%27round%27%3E%3Crect x=%2740%27 y=%2744%27 width=%27122%27 height=%2796%27 rx=%2716%27 fill=%27%238037e8%27/%3E%3Crect x=%2782%27 y=%2788%27 width=%27118%27 height=%2792%27 rx=%2716%27 fill=%27%2306b6d4%27/%3E%3Crect x=%2758%27 y=%27132%27 width=%27108%27 height=%2772%27 rx=%2716%27 fill=%27%23fb923c%27/%3E%3C/g%3E%3C/svg%3E">
<body style="margin:0;background:#15171c;color:#e6e8ee;font-family:sans-serif;text-align:center;padding:60px 16px">
<h1 style="font-size:20px">`+html.EscapeString(title)+`</h1><p style="color:#9aa3b2">`+html.EscapeString(msg)+`</p></body>`)
}
