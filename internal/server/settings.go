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
	"github.com/compico/overlaycomposer/internal/bundle/content"
	"github.com/compico/overlaycomposer/internal/deck"
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
func (s *Server) settingsSections() []bundle.Section {
	if s.d.Store == nil {
		return nil
	}
	return []bundle.Section{
		&content.Sources{Store: s.d.Store, Settings: s.sourcePluginSettings, Changed: func() {
			if s.d.Hub != nil {
				s.d.Hub.SourcesChanged()
			}
		}},
		&content.Deck{Store: s.deckBundleStore(), Scenes: func() []string {
			if o := s.deckObs(); o != nil && o.Connected() {
				return o.Scenes()
			}
			return nil
		}, PluginKnown: func(id string) bool { return s.d.Wasm != nil && s.d.Wasm.Has(id) }},
		&content.Layouts{Store: s.d.Store, Sources: s.d.Store,
			Default: func() string { id, _ := s.d.Store.DefaultLayoutID(); return id },
			Changed: func(ids []string) {
				if s.d.Hub == nil {
					return
				}
				for _, id := range ids {
					s.d.Hub.LayoutUpdated(id)
				}
				s.d.Hub.LayoutsChanged()
				s.d.Hub.Reresolve()
			}},
	}
}

// deckBundleStore — раскладка Deck для секции настроек: сохранение под тем же замком и с тем же счётчиком ревизий, что и у редактора панели.
type deckBundleStore struct{ s *Server }

func (s *Server) deckBundleStore() content.DeckStore { return deckBundleStore{s} }

func (d deckBundleStore) Layout() (deck.Layout, bool) {
	d.s.deckMu.Lock()
	defer d.s.deckMu.Unlock()
	return d.s.deckLayout()
}

func (d deckBundleStore) Save(l deck.Layout) error {
	d.s.deckMu.Lock()
	defer d.s.deckMu.Unlock()
	cur, _ := d.s.deckLayout()
	l.Rev = cur.Rev + 1 // выше текущей ревизии: устройства перерисуются
	raw, err := json.Marshal(l)
	if err != nil {
		return err
	}
	if err := d.s.d.Store.SetSetting(deckLayoutKey, string(raw)); err != nil {
		return err
	}
	d.s.deckPoke()
	return nil
}

// sourcePluginSettings проверяет настройки источника-плагина по манифесту (как при создании источника); known=false — плагина нет.
func (s *Server) sourcePluginSettings(plugin, widget string, in map[string]any) (map[string]any, bool, error) {
	p, ok := s.sourceManifest(plugin, widget)
	if !ok {
		return nil, false, nil
	}
	out, err := p.CleanSettings(in)
	return out, true, err
}

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
