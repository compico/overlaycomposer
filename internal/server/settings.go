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
	"github.com/compico/overlaycomposer/internal/config"
	"github.com/compico/overlaycomposer/internal/deck"
	"github.com/compico/overlaycomposer/internal/market"
	"github.com/compico/overlaycomposer/internal/wasmplugins"
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
	secs := []bundle.Section{}
	if s.d.Market != nil {
		secs = append(secs, &content.Store{List: s.d.Market}) // источники раньше плагинов: lock ссылается на них
	}
	if s.d.Market != nil && s.d.Wasm != nil {
		host := s.pluginBundleHost()
		// плагины ставятся первыми: источники и Deck ссылаются на них; настройки плагина — после установки
		secs = append(secs, &content.Plugins{Host: host}, &content.PluginSettingsSection{Host: host})
	}
	secs = append(secs,
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
	)
	if s.d.Cfg != nil {
		secs = append(secs, &content.App{Obs: obsBundleConfig{s}})
	}
	return secs
}

// obsBundleConfig — настройки OBS для секции app; применяются так же, как из окна настроек (переподключение сразу). Пароль не затрагивается.
type obsBundleConfig struct{ s *Server }

func (o obsBundleConfig) Get() content.ObsSettings {
	c := o.s.d.Cfg.Get()
	return content.ObsSettings{Enabled: c.Obs.Enabled, URL: c.Obs.URL}
}

func (o obsBundleConfig) Apply(v content.ObsSettings) error {
	prev, next, err := o.s.d.Cfg.Apply(config.Patch{Obs: &config.ObsPatch{Enabled: &v.Enabled, URL: &v.URL}})
	if err != nil {
		return err
	}
	o.s.configApplied(prev, next)
	return nil
}

// pluginBundleHost — магазин и менеджер wasm-плагинов для секций plugins и plugin-settings.
type pluginBundleHost struct{ s *Server }

func (s *Server) pluginBundleHost() content.PluginHost { return pluginBundleHost{s} }

func (h pluginBundleHost) Lock() market.WasmLockFile { return h.s.d.Market.WasmLockExport() }
func (h pluginBundleHost) Plan(ctx context.Context, f market.WasmLockFile) []market.WasmLockPlanItem {
	return h.s.d.Market.WasmLockPlan(ctx, f)
}
func (h pluginBundleHost) Install(ctx context.Context, repo, id, tag, sha string) error {
	_, err := h.s.d.Market.WasmInstall(ctx, repo, id, tag, sha)
	return err
}
func (h pluginBundleHost) info(id string) (wasmplugins.Info, bool) {
	list, _ := h.s.d.Wasm.List()
	for _, i := range list {
		if i.Meta.ID == id {
			return i, true
		}
	}
	return wasmplugins.Info{}, false
}
func (h pluginBundleHost) Loaded(id string) (content.LoadedPlugin, bool) {
	i, ok := h.info(id)
	if !ok {
		return content.LoadedPlugin{}, false
	}
	return content.LoadedPlugin{SHA256: i.SHA256, Requested: i.Meta.Permissions, Granted: i.Granted, Config: i.Config}, true
}
func (h pluginBundleHost) LoadedIDs() []string {
	list, _ := h.s.d.Wasm.List()
	ids := make([]string, 0, len(list))
	for _, i := range list {
		ids = append(ids, i.Meta.ID)
	}
	return ids
}
func (h pluginBundleHost) SetGrants(id string, perms []string) error {
	return h.s.d.Wasm.SetGrants(id, perms)
}
func (h pluginBundleHost) SetConfig(id string, cfg json.RawMessage) error {
	return h.s.d.Wasm.SetConfig(id, cfg)
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
	f, err := s.settings().ExportAll(ctx) // в бэкапе — всё, включая необязательные секции: он остаётся на этом компьютере
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
