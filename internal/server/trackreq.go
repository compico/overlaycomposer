package server

import (
	"errors"
	"strconv"

	"github.com/gofiber/fiber/v3"

	"github.com/compico/overlaycomposer/internal/trackreq"
	"github.com/compico/overlaycomposer/internal/twitch"
)

// Заявки на треки (награда за баллы канала → очередь плеера). Настройки и список — только с этого компьютера:
// награды создаются от имени стримера.
func (s *Server) trackReqRoutes() {
	app := s.app
	app.Get("/api/trackreq", s.localOnly, s.trackReqStatus)
	app.Put("/api/trackreq/config", s.localOnly, s.appGuard, s.trackReqSetConfig)
	app.Get("/api/trackreq/rewards", s.localOnly, s.trackReqRewards)
	app.Post("/api/trackreq/rewards", s.localOnly, s.appGuard, s.trackReqCreateReward)
	app.Post("/api/trackreq/requests/:id/cancel", s.localOnly, s.appGuard, s.trackReqCancel)
}

func (s *Server) trackReqUnavailable(c fiber.Ctx) bool {
	if s.d.TrackReq == nil {
		_ = c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "track requests are not available"})
		return true
	}
	return false
}

func (s *Server) trackReqStatus(c fiber.Ctx) error {
	if s.trackReqUnavailable(c) {
		return nil
	}
	reqs, err := s.d.TrackReq.Requests(50)
	if err != nil {
		return serverErr(c, err)
	}
	if reqs == nil {
		reqs = []trackreq.Request{}
	}
	return c.JSON(fiber.Map{"status": s.d.TrackReq.Status(), "requests": reqs})
}

func (s *Server) trackReqSetConfig(c fiber.Ctx) error {
	if s.trackReqUnavailable(c) {
		return nil
	}
	var b trackreq.Config
	if !body(c, &b) {
		return bad(c, "invalid body")
	}
	if b.Enabled && b.RewardID == "" {
		return bad(c, "choose a reward before enabling")
	}
	if err := s.d.TrackReq.SetConfig(b); err != nil {
		return serverErr(c, err)
	}
	return c.JSON(s.d.TrackReq.Status())
}

// twitchErr превращает ошибку слоя Twitch в понятный ответ: нет аккаунта/прав — 409, остальное — 502.
func twitchErr(c fiber.Ctx, err error) error {
	switch {
	case errors.Is(err, twitch.ErrNoAccount), errors.Is(err, twitch.ErrNeedScopes):
		return c.Status(fiber.StatusConflict).JSON(fiber.Map{"error": err.Error()})
	default:
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{"error": err.Error()})
	}
}

func (s *Server) trackReqRewards(c fiber.Ctx) error {
	if s.d.Twitch == nil {
		return bad(c, "twitch is not available")
	}
	list, err := s.d.Twitch.Rewards(c.Context())
	if err != nil {
		return twitchErr(c, err)
	}
	return c.JSON(list)
}

func (s *Server) trackReqCreateReward(c fiber.Ctx) error {
	if s.trackReqUnavailable(c) || s.d.Twitch == nil {
		return nil
	}
	var b struct {
		Title  string `json:"title"`
		Cost   int    `json:"cost"`
		Prompt string `json:"prompt"`
	}
	if !body(c, &b) {
		return bad(c, "invalid body")
	}
	r, err := s.d.Twitch.CreateReward(c.Context(), twitch.NewReward{Title: b.Title, Cost: b.Cost, Prompt: b.Prompt, InputRequired: true})
	if err != nil {
		if errors.Is(err, twitch.ErrNoAccount) || errors.Is(err, twitch.ErrNeedScopes) {
			return twitchErr(c, err)
		}
		return bad(c, err.Error())
	}
	return c.JSON(r)
}

func (s *Server) trackReqCancel(c fiber.Ctx) error {
	if s.trackReqUnavailable(c) {
		return nil
	}
	id, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil || id < 1 {
		return bad(c, "invalid id")
	}
	if err := s.d.TrackReq.Cancel(id); err != nil {
		if errors.Is(err, trackreq.ErrUnknownRequest) {
			return notFound(c)
		}
		return bad(c, err.Error())
	}
	return c.JSON(fiber.Map{"ok": true})
}
