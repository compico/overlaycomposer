package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"

	"github.com/compico/overlaycomposer/internal/bundle"
)

// Экспорт и импорт настроек одним файлом (internal/bundle, #98). Секции подключаются в settingsSections по мере готовности
// (layouts/sources, deck, plugins…). Только с этого компьютера: настройки могут раскрывать адреса и состав плагинов, а импорт меняет данные.

const settingsBackupKeep = 10

func (s *Server) settingsRoutes() {
	app := s.app
	app.Get("/api/settings/export", s.localOnly, s.settingsExport)
	app.Post("/api/settings/import/plan", s.localOnly, s.appGuard, s.settingsPlan)
	app.Post("/api/settings/import/apply", s.localOnly, s.appGuard, s.settingsApply)
}

// settingsSections — секции в порядке применения: зависимости раньше зависимых (источники до layouts, плагины до их настроек).
func (s *Server) settingsSections() []bundle.Section { return nil }

func (s *Server) settings() *bundle.Service {
	s.bundleOnce.Do(func() {
		svc := bundle.New(s.d.App.Version, s.settingsSections()...)
		svc.BeforeApply = s.backupSettings
		s.bundleSvc = svc
	})
	return s.bundleSvc
}

// backupSettings сохраняет текущие настройки в <data>/backups перед импортом (последние settingsBackupKeep файлов).
func (s *Server) backupSettings(ctx context.Context) error {
	f, err := s.settings().Export(ctx, nil)
	if err != nil {
		return err
	}
	path, err := bundle.WriteBackup(filepath.Join(s.d.Paths.DataDir, "backups"), f, time.Now(), settingsBackupKeep)
	if err == nil {
		slog.Info("настройки: бэкап перед импортом", "file", path)
	}
	return err
}

// GET /api/settings/export?sections=layouts,deck — файл настроек (пусто — все секции). Заголовок скачивания, если ?download=1.
func (s *Server) settingsExport(c fiber.Ctx) error {
	var names []string
	for _, n := range strings.Split(c.Query("sections"), ",") {
		if n = strings.TrimSpace(n); n != "" {
			names = append(names, n)
		}
	}
	f, err := s.settings().Export(c.Context(), names)
	if err != nil {
		return bad(c, err.Error())
	}
	if c.Query("download") == "1" {
		c.Set("Content-Disposition", `attachment; filename="oc-settings.json"`)
	}
	return c.JSON(f)
}

// POST /api/settings/import/plan {файл настроек} — проверка и оценка по секциям; ничего не меняет.
func (s *Server) settingsPlan(c fiber.Ctx) error {
	f, err := bundle.Parse(c.Body())
	if err != nil {
		return bad(c, err.Error())
	}
	return c.JSON(fiber.Map{"format": f.Format, "app": f.App, "createdAt": f.CreatedAt, "sections": s.settings().Plan(c.Context(), f)})
}

// POST /api/settings/import/apply {file, selection:{секция:{id:"replace"|"copy"}}} — применяет выбранное, перед этим делает бэкап.
func (s *Server) settingsApply(c fiber.Ctx) error {
	var b struct {
		File      json.RawMessage  `json:"file"`
		Selection bundle.Selection `json:"selection"`
	}
	if !body(c, &b) || len(b.File) == 0 {
		return bad(c, "invalid json")
	}
	f, err := bundle.Parse(b.File)
	if err != nil {
		return bad(c, err.Error())
	}
	reports, err := s.settings().Apply(c.Context(), f, b.Selection)
	if err != nil {
		return bad(c, err.Error())
	}
	slog.Info("настройки: импорт применён", "sections", len(reports))
	return c.JSON(fiber.Map{"reports": reports})
}
