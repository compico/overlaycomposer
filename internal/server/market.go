package server

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/gofiber/fiber/v3"

	"github.com/compico/overlaycomposer/internal/market"
)

// Магазин плагинов — только с этого компьютера: ставить чужой код на машину стримера может лишь он сам,
// а не устройство, сопряжённое по сети.
func (s *Server) marketRoutes() {
	app := s.app
	app.Get("/api/market", s.localOnly, s.marketCatalog)
	app.Post("/api/market/sources", s.localOnly, s.appGuard, s.marketAddSource)
	app.Delete("/api/market/sources", s.localOnly, s.appGuard, s.marketRemoveSource)
	app.Put("/api/market/token", s.localOnly, s.appGuard, s.marketSetToken)
	app.Delete("/api/market/token", s.localOnly, s.appGuard, s.marketClearToken)
	app.Post("/api/market/install", s.localOnly, s.appGuard, s.marketInstall)
	app.Post("/api/market/uninstall", s.localOnly, s.appGuard, s.marketUninstall)
}

func marketCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 45*time.Second)
}

func (s *Server) marketCatalog(c fiber.Ctx) error {
	if s.d.Market == nil {
		return bad(c, "store is unavailable")
	}
	ctx, cancel := marketCtx()
	defer cancel()
	inst := s.d.Market.Installed()
	return c.JSON(fiber.Map{
		"sources":   s.d.Market.Catalog(ctx, c.Query("refresh") == "1"),
		"installed": inst,
		"hasToken":  s.d.Market.HasToken(), // сам токен наружу не отдаётся никогда
	})
}

func marketErr(c fiber.Ctx, err error) error {
	switch {
	case errors.Is(err, market.ErrBadRepo), errors.Is(err, market.ErrNoSource), errors.Is(err, market.ErrNoPlugin),
		errors.Is(err, market.ErrConflict), errors.Is(err, market.ErrNotOurs), errors.Is(err, market.ErrChanged),
		errors.Is(err, market.ErrToken), errors.Is(err, market.ErrNoBox):
		return bad(c, err.Error())
	}
	slog.Warn("store", "err", err)
	return bad(c, err.Error()) // ошибки сети/архива — полезны пользователю как есть, секретов в них нет
}

func (s *Server) marketAddSource(c fiber.Ctx) error {
	var b struct {
		Repo string `json:"repo"`
	}
	if !body(c, &b) {
		return bad(c, "invalid json")
	}
	ctx, cancel := marketCtx()
	defer cancel()
	repo, err := s.d.Market.AddSource(ctx, b.Repo)
	if err != nil {
		return marketErr(c, err)
	}
	return c.JSON(fiber.Map{"repo": repo})
}

func (s *Server) marketRemoveSource(c fiber.Ctx) error {
	if err := s.d.Market.RemoveSource(c.Query("repo")); err != nil {
		return marketErr(c, err)
	}
	return c.JSON(fiber.Map{"ok": true})
}

func (s *Server) marketInstall(c fiber.Ctx) error {
	var b struct {
		Repo   string `json:"repo"`
		ID     string `json:"id"`
		Digest string `json:"digest"` // отпечаток из каталога: если плагин с тех пор изменился, установка отклоняется
	}
	if !body(c, &b) {
		return bad(c, "invalid json")
	}
	ctx, cancel := marketCtx()
	defer cancel()
	e, err := s.d.Market.Install(ctx, b.Repo, b.ID, b.Digest)
	if err != nil {
		return marketErr(c, err)
	}
	slog.Info("store: плагин установлен", "id", e.ID, "version", e.Version, "repo", e.Repo)
	return c.JSON(e)
}

func (s *Server) marketSetToken(c fiber.Ctx) error {
	var b struct {
		Token string `json:"token"`
	}
	if !body(c, &b) {
		return bad(c, "invalid json")
	}
	if err := s.d.Market.SetToken(b.Token); err != nil {
		return marketErr(c, err)
	}
	slog.Info("store: токен GitHub сохранён")
	return c.JSON(fiber.Map{"hasToken": true})
}

func (s *Server) marketClearToken(c fiber.Ctx) error {
	if err := s.d.Market.ClearToken(); err != nil {
		return marketErr(c, err)
	}
	return c.JSON(fiber.Map{"hasToken": false})
}

func (s *Server) marketUninstall(c fiber.Ctx) error {
	var b struct {
		ID string `json:"id"`
	}
	if !body(c, &b) {
		return bad(c, "invalid json")
	}
	if err := s.d.Market.Uninstall(b.ID); err != nil {
		return marketErr(c, err)
	}
	slog.Info("store: плагин удалён", "id", b.ID)
	return c.JSON(fiber.Map{"ok": true})
}
