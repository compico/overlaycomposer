package server

import (
	"encoding/json"
	"errors"
	"log/slog"
	"strings"

	"github.com/gofiber/fiber/v3"

	"github.com/compico/overlaycomposer/internal/twitch"
	"github.com/compico/overlaycomposer/internal/wasmplugins"
)

const wasmMaxBody = 1 << 20

func (s *Server) wasmRoutes() {
	app := s.app
	// Управление плагинами и награды — только с этого компьютера: плагин получает права от имени пользователя.
	app.Get("/api/wasmplugins", s.localOnly, s.wasmList)
	app.Post("/api/wasmplugins/reload", s.localOnly, s.appGuard, s.wasmReload)
	app.Put("/api/wasmplugins/:id/grants", s.localOnly, s.appGuard, s.wasmGrants)
	app.Put("/api/wasmplugins/:id/config", s.localOnly, s.appGuard, s.wasmConfig)
	app.Post("/api/wasmplugins/:id/restart", s.localOnly, s.appGuard, s.wasmRestart)
	app.Get("/api/twitch/rewards", s.localOnly, s.twitchRewards)
	app.Post("/api/twitch/rewards", s.localOnly, s.appGuard, s.twitchCreateReward)
}

// wasmPluginRoutes — HTTP-маршруты самих плагинов. Регистрируются раньше раздачи файлов виджетов (/plugins/:id/*).
// Общая защита (originGuard, authGuard) уже действует: запись из sandbox-iframe чужого origin отклоняется,
// с других устройств нужен токен. Обработчики GET должны быть без побочных эффектов — их можно вызвать с любой страницы.
func (s *Server) wasmPluginRoutes() {
	s.app.All("/plugins/:id/api", s.wasmRoute)
	s.app.All("/plugins/:id/api/*", s.wasmRoute)
}

func (s *Server) wasmRoute(c fiber.Ctx) error {
	id := c.Params("id")
	if s.d.Wasm == nil || !s.d.Wasm.Has(id) {
		if c.Method() == fiber.MethodGet {
			return c.Next() // это может быть файл HTML-плагина с папкой api
		}
		return notFound(c)
	}
	req := wasmplugins.Request{Method: c.Method(), Path: "/" + strings.Clone(c.Params("*")), Query: map[string]string{}}
	for k, v := range c.Queries() { // Fiber переиспользует буферы запроса — копируем
		req.Query[strings.Clone(k)] = strings.Clone(v)
	}
	if b := c.Body(); len(b) > 0 {
		if len(b) > wasmMaxBody {
			return c.Status(fiber.StatusRequestEntityTooLarge).JSON(fiber.Map{"error": "request body is too large"})
		}
		if !json.Valid(b) {
			return bad(c, "body must be JSON")
		}
		req.Body = append(json.RawMessage(nil), b...)
	}
	res, err := s.d.Wasm.Handle(c.Context(), id, req)
	switch {
	case err == nil:
	case errors.Is(err, wasmplugins.ErrNoRoute):
		return notFound(c)
	case errors.Is(err, wasmplugins.ErrUnavailable):
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "plugin is not running"})
	default:
		slog.Warn("wasm plugin: ошибка обработчика", "plugin", id, "path", req.Path, "err", err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "plugin error"})
	}
	// Тип ответа фиксирован: плагин отдаёт JSON, а не страницы, которые могли бы выполниться на origin приложения.
	c.Set("Cache-Control", "no-store")
	c.Set("X-Content-Type-Options", "nosniff")
	c.Status(res.Status)
	if res.Status == fiber.StatusNoContent || len(res.Body) == 0 {
		return nil
	}
	c.Set("Content-Type", "application/json; charset=utf-8")
	return c.Send(res.Body)
}

func (s *Server) wasmUnavailable(c fiber.Ctx) bool {
	if s.d.Wasm == nil {
		_ = c.Status(fiber.StatusNotImplemented).JSON(fiber.Map{"error": "wasm plugins are not available"})
		return true
	}
	return false
}

func (s *Server) wasmList(c fiber.Ctx) error {
	if s.wasmUnavailable(c) {
		return nil
	}
	infos, problems := s.d.Wasm.List()
	if problems == nil {
		problems = []wasmplugins.Problem{}
	}
	st, detail := "off", ""
	if s.redeem != nil {
		st, detail = s.redeem.State()
	}
	return c.JSON(fiber.Map{"plugins": infos, "problems": problems, "dirs": s.d.Wasm.Dirs(), "twitch": fiber.Map{"state": st, "detail": detail}})
}

func (s *Server) wasmReload(c fiber.Ctx) error {
	if s.wasmUnavailable(c) {
		return nil
	}
	s.d.Wasm.Scan()
	s.SyncWasm()
	return s.wasmList(c)
}

// wasmErr сопоставляет ошибки менеджера кодам ответа.
func wasmErr(c fiber.Ctx, err error) error {
	if errors.Is(err, wasmplugins.ErrNoPlugin) {
		return notFound(c)
	}
	return bad(c, err.Error())
}

func (s *Server) wasmGrants(c fiber.Ctx) error {
	if s.wasmUnavailable(c) {
		return nil
	}
	var b struct {
		Granted []string `json:"granted"`
	}
	if !body(c, &b) {
		return bad(c, "invalid json")
	}
	if err := s.d.Wasm.SetGrants(c.Params("id"), b.Granted); err != nil {
		return wasmErr(c, err)
	}
	s.SyncWasm()
	return s.wasmList(c)
}

func (s *Server) wasmConfig(c fiber.Ctx) error {
	if s.wasmUnavailable(c) {
		return nil
	}
	if err := s.d.Wasm.SetConfig(c.Params("id"), append(json.RawMessage(nil), c.Body()...)); err != nil {
		return wasmErr(c, err)
	}
	return s.wasmList(c)
}

func (s *Server) wasmRestart(c fiber.Ctx) error {
	if s.wasmUnavailable(c) {
		return nil
	}
	if err := s.d.Wasm.Restart(c.Params("id")); err != nil {
		if errors.Is(err, wasmplugins.ErrNoPlugin) {
			return notFound(c)
		}
		slog.Warn("wasm plugin: перезапуск не удался", "plugin", c.Params("id"), "err", err)
	}
	return s.wasmList(c) // состояние (в том числе failed и причина) видно в списке
}

// ---- награды ----

func twitchRewardErr(c fiber.Ctx, err error) error {
	switch {
	case errors.Is(err, twitch.ErrNoAccount), errors.Is(err, twitch.ErrNeedScopes):
		return c.Status(fiber.StatusConflict).JSON(fiber.Map{"error": err.Error()})
	case errors.Is(err, twitch.ErrInvalidReward):
		return bad(c, err.Error())
	}
	slog.Warn("twitch: награды", "err", err)
	return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{"error": err.Error()})
}

func (s *Server) twitchRewards(c fiber.Ctx) error {
	rw, err := s.d.Twitch.Rewards(c.Context())
	if err != nil {
		return twitchRewardErr(c, err)
	}
	return c.JSON(fiber.Map{"rewards": rw})
}

func (s *Server) twitchCreateReward(c fiber.Ctx) error {
	var b struct {
		Title         string `json:"title"`
		Cost          int    `json:"cost"`
		Prompt        string `json:"prompt"`
		InputRequired bool   `json:"inputRequired"`
	}
	if !body(c, &b) {
		return bad(c, "invalid json")
	}
	rw, err := s.d.Twitch.CreateReward(c.Context(), twitch.NewReward{Title: b.Title, Cost: b.Cost, Prompt: b.Prompt, InputRequired: b.InputRequired})
	if err != nil {
		return twitchRewardErr(c, err)
	}
	return c.JSON(rw)
}

// SyncWasm приводит мост EventSub в соответствие с плагинами: вызывать после запуска и после изменений прав.
func (s *Server) SyncWasm() {
	if s.redeem == nil || s.d.Wasm == nil {
		return
	}
	s.redeem.Sync(s.d.Wasm.Wants(wasmplugins.EventRedemption))
}
