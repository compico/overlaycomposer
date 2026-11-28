package server

import (
	"encoding/json"
	"log/slog"
	"slices"
	"strconv"

	"github.com/gofiber/fiber/v3"

	"github.com/compico/overlaycomposer/internal/dashboard"
	"github.com/compico/overlaycomposer/internal/wasmplugins"
)

// Раскладка окна Dashboard (internal/dashboard): настройка dashboard_layout в БД, как у Deck. Ревизия защищает от затирания
// правок из второго окна; счётчик dashboard_rev растёт при сбросе, чтобы окно с прежним номером заметило новое сохранение.
const (
	dashboardLayoutKey = "dashboard_layout"
	dashboardRevKey    = "dashboard_rev"
)

func (s *Server) dashboardRoutes() {
	app := s.app
	app.Get("/api/dashboard/layout", s.dashboardGetLayout)
	app.Put("/api/dashboard/layout", s.appGuard, s.dashboardPutLayout)
	app.Delete("/api/dashboard/layout", s.appGuard, s.dashboardResetLayout)
}

// dashboardWidgets — виджеты загруженных плагинов, которые можно показывать в окне (область dashboard).
func (s *Server) dashboardWidgets() []dashboard.Widget {
	out := []dashboard.Widget{}
	for _, m := range s.wasmManifests() {
		if slices.Contains(m.Surfaces, wasmplugins.SurfaceDashboard) {
			out = append(out, dashboard.Widget{Plugin: m.ID, Widget: m.Widget, Name: m.Name})
		}
	}
	return out
}

// dashboardLayout отдаёт сохранённую раскладку, а если её нет или она не читается — раскладку по умолчанию.
func (s *Server) dashboardLayout() (l dashboard.Layout, saved bool) {
	if raw, ok, err := s.d.Store.Setting(dashboardLayoutKey); err == nil && ok && raw != "" {
		if l, err = dashboard.Parse([]byte(raw)); err == nil {
			return l, true
		}
		slog.Warn("dashboard: saved layout is invalid, using the default", "err", err)
	}
	l = dashboard.Default(s.dashboardWidgets())
	if raw, ok, err := s.d.Store.Setting(dashboardRevKey); err == nil && ok {
		l.Rev, _ = strconv.Atoi(raw)
	}
	return l, false
}

func (s *Server) dashboardGetLayout(c fiber.Ctx) error {
	l, saved := s.dashboardLayout()
	return c.JSON(fiber.Map{
		"layout": l, "saved": saved, "widgets": s.dashboardWidgets(),
		"grid": fiber.Map{"cols": dashboard.Cols, "maxRows": dashboard.MaxRows, "rowPx": dashboard.RowPx, "maxPanels": dashboard.MaxPanels},
	})
}

// dashboardPutLayout сохраняет раскладку. rev в теле должен совпасть с текущим; новый rev ставит сервер.
func (s *Server) dashboardPutLayout(c fiber.Ctx) error {
	in, err := dashboard.Parse(c.Body())
	if err != nil {
		return bad(c, err.Error())
	}
	s.dashMu.Lock()
	defer s.dashMu.Unlock()
	cur, _ := s.dashboardLayout()
	if in.Rev != cur.Rev {
		return c.Status(fiber.StatusConflict).JSON(fiber.Map{"error": "the layout was changed elsewhere", "rev": cur.Rev})
	}
	in.Rev = cur.Rev + 1
	raw, err := json.Marshal(in)
	if err != nil {
		return err
	}
	if err := s.d.Store.SetSetting(dashboardLayoutKey, string(raw)); err != nil {
		return err
	}
	return c.JSON(fiber.Map{"ok": true, "rev": in.Rev})
}

// dashboardResetLayout возвращает раскладку по умолчанию; счётчик ревизий не откатывается (dashboard_rev).
func (s *Server) dashboardResetLayout(c fiber.Ctx) error {
	s.dashMu.Lock()
	defer s.dashMu.Unlock()
	cur, saved := s.dashboardLayout()
	if !saved {
		return c.JSON(fiber.Map{"ok": true, "rev": cur.Rev})
	}
	if err := s.d.Store.SetSetting(dashboardRevKey, strconv.Itoa(cur.Rev+1)); err != nil {
		return err
	}
	if err := s.d.Store.SetSetting(dashboardLayoutKey, ""); err != nil {
		return err
	}
	return c.JSON(fiber.Map{"ok": true, "rev": cur.Rev + 1})
}
