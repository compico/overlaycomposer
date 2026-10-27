package server

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/gofiber/fiber/v3"

	"github.com/compico/overlaycomposer/internal/notify"
)

// Центр уведомлений — только с этого компьютера (как и магазин): внутри пути, версии, ссылки.
func (s *Server) notifyRoutes() {
	app := s.app
	app.Get("/api/notifications", s.localOnly, s.notifyList)
	app.Post("/api/notifications/read", s.localOnly, s.appGuard, s.notifyRead)
	app.Post("/api/notifications/dismiss", s.localOnly, s.appGuard, s.notifyDismiss)
	app.Post("/api/notifications/check", s.localOnly, s.appGuard, s.notifyCheck)
}

func (s *Server) notifyView() fiber.Map {
	last := int64(0)
	if t := s.d.Notify.LastCheck(); !t.IsZero() {
		last = t.Unix()
	}
	return fiber.Map{"items": s.d.Notify.List(), "unread": s.d.Notify.Unread(), "lastCheck": last}
}

func (s *Server) notifyList(c fiber.Ctx) error {
	if s.d.Notify == nil {
		return c.JSON(fiber.Map{"items": []notify.Item{}, "unread": 0, "lastCheck": 0})
	}
	return c.JSON(s.notifyView())
}

type notifyKey struct {
	Key string `json:"key"` // пусто — все
}

func (s *Server) notifyRead(c fiber.Ctx) error {
	var b notifyKey
	if s.d.Notify == nil || !body(c, &b) {
		return bad(c, "invalid request")
	}
	s.d.Notify.MarkRead(b.Key)
	return c.JSON(s.notifyView())
}

func (s *Server) notifyDismiss(c fiber.Ctx) error {
	var b notifyKey
	if s.d.Notify == nil || !body(c, &b) {
		return bad(c, "invalid request")
	}
	s.d.Notify.Dismiss(b.Key)
	return c.JSON(s.notifyView())
}

// notifyCheck — кнопка «Проверить сейчас»: проверка обновлений плагинов не чаще раза в минуту.
func (s *Server) notifyCheck(c fiber.Ctx) error {
	if s.d.Notify == nil || s.d.CheckPlugins == nil {
		return bad(c, "update check is unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	switch err := s.d.CheckPlugins(ctx, true); {
	case err == nil:
	case errors.Is(err, notify.ErrTooSoon):
		return c.Status(fiber.StatusTooManyRequests).JSON(fiber.Map{"error": "checked a moment ago, try again in a minute"})
	case errors.Is(err, notify.ErrChecking):
		return c.Status(fiber.StatusTooManyRequests).JSON(fiber.Map{"error": "a check is already running"})
	default:
		slog.Warn("проверка обновлений плагинов", "err", err)
		return bad(c, "could not reach the plugin sources (see log)")
	}
	return c.JSON(s.notifyView())
}
