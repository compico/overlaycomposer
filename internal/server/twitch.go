package server

import (
	"errors"
	"html"
	"log/slog"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v3"

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
}

// twitchRedirectURI — Twitch разрешает http только для localhost, поэтому имя именно localhost (а не 127.0.0.1).
func (s *Server) twitchRedirectURI() string {
	return "http://localhost:" + strconv.Itoa(s.d.Running.Port) + "/auth/twitch/callback"
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
	if accs == nil {
		accs = []twitch.Account{}
	}
	return c.JSON(fiber.Map{
		"clientId":    id,
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
	u, err := s.d.Twitch.AuthURL(c.Params("role"), s.twitchRedirectURI())
	switch {
	case err == nil:
		return c.JSON(fiber.Map{"url": u})
	case errors.Is(err, twitch.ErrBadRole), errors.Is(err, twitch.ErrNotConfigured):
		return bad(c, err.Error())
	}
	return serverErr(c, err)
}

func (s *Server) twitchDisconnect(c fiber.Ctx) error {
	err := s.d.Twitch.Disconnect(c.Context(), c.Params("role"))
	switch {
	case err == nil:
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
	return s.twitchResult(c, fiber.StatusOK, "Connected as "+acc.Login, "Role: "+acc.Role+". You can close this tab and return to Overlay Composer.")
}

func (s *Server) twitchResult(c fiber.Ctx, status int, title, msg string) error {
	c.Status(status)
	c.Set("Cache-Control", "no-store")
	return s.htmlString(c, `<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Overlay Composer — Twitch</title>
<body style="margin:0;background:#15171c;color:#e6e8ee;font-family:sans-serif;text-align:center;padding:60px 16px">
<h1 style="font-size:20px">`+html.EscapeString(title)+`</h1><p style="color:#9aa3b2">`+html.EscapeString(msg)+`</p></body>`)
}
