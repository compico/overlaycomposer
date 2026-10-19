package server

import (
	"fmt"
	"strings"

	"github.com/gofiber/fiber/v3"

	"github.com/compico/overlaycomposer/internal/model"
	"github.com/compico/overlaycomposer/internal/obsimport"
)

// POST /api/obs/import {scene, bind?} — сцена OBS → новый layout (+ новые sources). Нужен obs-websocket.
func (s *Server) importScene(c fiber.Ctx) error {
	var b struct {
		Scene string `json:"scene"`
		Bind  *bool  `json:"bind"` // привязать layout к этой сцене (по умолчанию да, если сцена свободна)
	}
	if !body(c, &b) || strings.TrimSpace(b.Scene) == "" {
		return bad(c, "scene required")
	}
	if !s.d.Obs.Connected() {
		return bad(c, "Для импорта нужно подключение к OBS по obs-websocket (Settings → OBS).")
	}
	existing, err := s.d.Store.ListSources()
	if err != nil {
		return serverErr(c, err)
	}
	res, err := obsimport.Import(s.d.Obs, b.Scene, existing)
	if err != nil {
		return bad(c, err.Error())
	}

	layouts, err := s.d.Store.ListLayouts()
	if err != nil {
		return serverErr(c, err)
	}
	ids := map[string]bool{}
	for _, l := range layouts {
		ids[l.ID] = true
	}
	id := model.Slugify(b.Scene)
	for i := 2; ids[id]; i++ {
		id = fmt.Sprintf("%s-%d", model.Slugify(b.Scene), i)
	}
	res.Layout.ID = id

	bound := false
	if b.Bind == nil || *b.Bind {
		owner, err := s.d.Store.SceneOwner(b.Scene)
		if err != nil {
			return serverErr(c, err)
		}
		if owner == "" {
			res.Layout.Scenes = []string{b.Scene}
			bound = true
		} else {
			res.Warnings = append(res.Warnings, "сцена уже привязана к другому layout — привязка не менялась")
		}
	}
	if msg := res.Layout.Validate(); msg != "" {
		return bad(c, msg)
	}
	for _, src := range res.Sources {
		if err := s.d.Store.SaveSource(src); err != nil {
			return serverErr(c, err)
		}
	}
	if _, err := s.d.Store.SaveLayout(res.Layout); err != nil {
		return serverErr(c, err)
	}
	s.d.Hub.SourcesChanged()
	s.d.Hub.LayoutsChanged()
	s.d.Hub.Reresolve()
	return c.Status(fiber.StatusCreated).JSON(fiber.Map{
		"layout": res.Layout, "newSources": res.Sources, "reusedSources": res.Reused, "proxied": res.Proxied,
		"skipped": res.Skipped, "warnings": res.Warnings, "bound": bound,
	})
}
