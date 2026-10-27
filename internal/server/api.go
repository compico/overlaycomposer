package server

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"runtime"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"

	"github.com/compico/overlaycomposer/internal/config"
	"github.com/compico/overlaycomposer/internal/model"
)

func bad(c fiber.Ctx, msg string) error {
	return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": msg})
}

func notFound(c fiber.Ctx) error {
	return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "not found"})
}

func serverErr(c fiber.Ctx, err error) error {
	slog.Error("api", "path", c.Path(), "err", err)
	return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "internal error"})
}

// body разбирает JSON тела в v; false — тело пустое или битое.
func body(c fiber.Ctx, v any) bool {
	return len(c.Body()) > 0 && json.Unmarshal(c.Body(), v) == nil
}

func (s *Server) api() {
	app := s.app
	app.Get("/api/state", s.getState)

	app.Get("/api/default-layout", s.getDefaultLayout)
	app.Put("/api/default-layout", s.putDefaultLayout)

	app.Get("/api/obs/scenes", func(c fiber.Ctx) error {
		return c.JSON(fiber.Map{"scenes": s.d.Hub.SceneList(true)})
	})
	app.Post("/api/obs/reconnect", func(c fiber.Ctx) error {
		cfg := s.d.Cfg.Get()
		if cfg.Obs.Enabled {
			s.d.Obs.Reconnect(cfg.Obs.URL, cfg.Obs.Password)
		}
		return c.JSON(fiber.Map{"ok": true})
	})

	app.Post("/api/client-log", s.appGuard, s.clientLog)
	app.Post("/api/obs/import", s.importScene)

	app.Get("/api/config", func(c fiber.Ctx) error { return c.JSON(s.configView()) })
	app.Put("/api/config", s.putConfig)

	app.Get("/api/layouts", s.listLayouts)
	app.Post("/api/layouts", s.createLayout)
	app.Get("/api/layouts/:id", s.getLayout)
	app.Put("/api/layouts/:id", s.putLayout)
	app.Delete("/api/layouts/:id", s.deleteLayout)

	app.Get("/api/plugins", func(c fiber.Ctx) error { return c.JSON(s.d.Plugins.List()) })
	app.Get("/api/sources", s.listSources)
	app.Post("/api/sources", s.createSource)
	app.Put("/api/sources/:id", s.putSource)
	app.Delete("/api/sources/:id", s.deleteSource)

	// /api/app: GET открыт, изменяющие запросы требуют заголовок x-overlay-composer (см. appGuard).
	app.Get("/api/app", s.appInfo)
	app.Put("/api/app/autostart", s.appGuard, s.appAutostart)
	app.Post("/api/app/open", s.appGuard, s.appOpen)
	app.Post("/api/app/quit", s.appGuard, s.appQuit)

	app.All("/api/*", func(c fiber.Ctx) error { return notFound(c) })
}

// ---- state / default layout ----

func (s *Server) getState(c fiber.Ctx) error { return c.JSON(s.d.Hub.State()) }

func (s *Server) getDefaultLayout(c fiber.Ctx) error {
	id, err := s.d.Store.DefaultLayoutID()
	if err != nil {
		return serverErr(c, err)
	}
	return c.JSON(fiber.Map{"layoutId": nilIfEmpty(id)})
}

func (s *Server) putDefaultLayout(c fiber.Ctx) error {
	var b struct {
		LayoutID *string `json:"layoutId"`
	}
	if !body(c, &b) {
		return bad(c, "invalid json")
	}
	id := ""
	if b.LayoutID != nil {
		id = *b.LayoutID
		l, err := s.d.Store.GetLayout(id)
		if err != nil {
			return serverErr(c, err)
		}
		if l == nil {
			return bad(c, "unknown layout")
		}
	}
	if err := s.d.Store.SetDefaultLayoutID(id); err != nil {
		return serverErr(c, err)
	}
	s.d.Hub.LayoutsChanged()
	s.d.Hub.Reresolve()
	return c.JSON(fiber.Map{"layoutId": nilIfEmpty(id)})
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// ---- config ----

type configView struct {
	config.Public
	Obs             model.ObsInfo `json:"obs"`
	ObsWsConnected  bool          `json:"obsWsConnected"`
	ObsWsError      *string       `json:"obsWsError"`
	OverlayURL      string        `json:"overlayUrl"`
	RestartRequired bool          `json:"restartRequired"`
}

func (s *Server) configView() configView {
	cfg := s.d.Cfg.Get()
	return configView{
		Public:          cfg.Public(),
		Obs:             s.d.Hub.ObsInfo(),
		ObsWsConnected:  s.d.Obs.Connected(),
		ObsWsError:      nilIfEmpty(s.d.Obs.LastError()),
		OverlayURL:      s.OverlayURL(),
		RestartRequired: cfg.Host != s.d.Running.Host || cfg.Port != s.d.Running.Port,
	}
}

func (s *Server) putConfig(c fiber.Ctx) error {
	var p config.Patch
	if !body(c, &p) {
		return bad(c, "invalid json")
	}
	prev, next, err := s.d.Cfg.Apply(p)
	if err != nil {
		return bad(c, err.Error())
	}
	s.d.Proxy.SetDebug(next.DebugProxy)
	// Применяем сразу: включение/выключение obs-websocket и смена адреса/пароля.
	switch {
	case !next.Obs.Enabled:
		s.d.Obs.Stop()
	case !prev.Obs.Enabled || next.Obs.URL != prev.Obs.URL || next.Obs.Password != prev.Obs.Password:
		s.d.Obs.Reconnect(next.Obs.URL, next.Obs.Password)
	}
	s.d.Hub.BroadcastObsStatus()
	return c.JSON(s.configView())
}

// ---- layouts ----

func (s *Server) listLayouts(c fiber.Ctx) error {
	ls, err := s.d.Store.ListLayouts()
	if err != nil {
		return serverErr(c, err)
	}
	return c.JSON(ls)
}

func (s *Server) getLayout(c fiber.Ctx) error {
	l, err := s.d.Store.GetLayout(c.Params("id"))
	if err != nil {
		return serverErr(c, err)
	}
	if l == nil {
		return notFound(c)
	}
	return c.JSON(l)
}

func (s *Server) createLayout(c fiber.Ctx) error {
	var b struct {
		Name        string `json:"name"`
		DuplicateOf string `json:"duplicateOf"`
	}
	_ = body(c, &b)
	name := strings.TrimSpace(b.Name)
	if name == "" {
		name = "New Layout"
	}
	all, err := s.d.Store.ListLayouts()
	if err != nil {
		return serverErr(c, err)
	}
	existing, taken := map[string]bool{}, map[string]bool{}
	for _, l := range all {
		existing[l.ID] = true
		for _, sc := range l.Scenes {
			taken[sc] = true
		}
	}
	id := model.Slugify(name)
	for i := 2; existing[id]; i++ {
		id = fmt.Sprintf("%s-%d", model.Slugify(name), i)
	}

	var layout model.Layout
	if b.DuplicateOf != "" {
		src, err := s.d.Store.GetLayout(b.DuplicateOf)
		if err != nil {
			return serverErr(c, err)
		}
		if src == nil {
			return notFound(c)
		}
		layout = *src // GetLayout отдаёт свежую копию — менять можно
		layout.ID, layout.Name = id, name
		layout.Scenes = []string{} // сцена принадлежит одному layout — копия начинает без привязок
	} else {
		layout = model.Layout{ID: id, Name: name, Scenes: []string{}, Canvas: model.Canvas{Width: 1920, Height: 1080}, Objects: []model.Object{}}
		// Удобство: если в OBS есть сцена с таким же именем и она ещё не занята — привязываем сразу.
		if !taken[name] {
			for _, sc := range s.d.Hub.SceneList(false) {
				if sc == name {
					layout.Scenes = []string{name}
					break
				}
			}
		}
	}
	if msg := layout.Validate(); msg != "" {
		return bad(c, msg)
	}
	if _, err := s.d.Store.SaveLayout(layout); err != nil {
		return serverErr(c, err)
	}
	s.d.Hub.LayoutsChanged()
	s.d.Hub.Reresolve()
	return c.Status(fiber.StatusCreated).JSON(layout)
}

func (s *Server) putLayout(c fiber.Ctx) error {
	id := c.Params("id")
	var l model.Layout
	if !body(c, &l) {
		return bad(c, "invalid json")
	}
	l.ID = id
	if msg := l.Validate(); msg != "" {
		return bad(c, msg)
	}
	cur, err := s.d.Store.GetLayout(id)
	if err != nil {
		return serverErr(c, err)
	}
	if cur == nil {
		return notFound(c)
	}
	stolen, err := s.d.Store.SaveLayout(l) // привязки к сценам других layout переносятся внутри транзакции
	if err != nil {
		return serverErr(c, err)
	}
	s.d.Hub.LayoutUpdated(id)
	s.d.Hub.LayoutsChanged() // привязки (и имя) видны в списке layout
	if stolen {
		slog.Info("layouts: привязки сцен перенесены", "layout", l.Name)
	}
	s.d.Hub.Reresolve()
	return c.JSON(l)
}

func (s *Server) deleteLayout(c fiber.Ctx) error {
	id := c.Params("id")
	ok, err := s.d.Store.DeleteLayout(id)
	if err != nil {
		return serverErr(c, err)
	}
	if !ok {
		return notFound(c)
	}
	s.d.Hub.LayoutDeleted(id)
	s.d.Hub.LayoutsChanged()
	s.d.Hub.Reresolve()
	return c.JSON(fiber.Map{"ok": true})
}

// ---- sources ----

func (s *Server) listSources(c fiber.Ctx) error {
	list, err := s.d.Store.ListSources()
	if err != nil {
		return serverErr(c, err)
	}
	return c.JSON(list)
}

func (s *Server) createSource(c fiber.Ctx) error {
	var b model.Source
	if !body(c, &b) || !model.IsValidID(b.ID) || b.Name == "" || (b.Type != "iframe" && b.Type != "plugin") {
		return bad(c, "invalid source (type: iframe | plugin)")
	}
	if b.Type == "plugin" {
		p, ok := s.d.Plugins.Get(b.Data.Plugin)
		if !ok {
			return bad(c, "unknown plugin")
		}
		settings, err := p.CleanSettings(b.Data.Settings)
		if err != nil {
			return bad(c, "invalid settings: "+err.Error())
		}
		granted, err := p.CleanGranted(b.Data.Granted)
		if err != nil {
			return bad(c, err.Error())
		}
		b.Data = model.SourceData{Plugin: p.ID, Settings: settings, Granted: granted} // URL и proxy у плагина не бывает
	} else {
		if !model.SafeURL(b.Data.URL) {
			return bad(c, "invalid url (only http/https or /path)")
		}
		if b.Data.Proxy && !model.IsAbsHTTP(b.Data.URL) {
			return bad(c, "proxy needs an absolute http(s) URL")
		}
		b.Data.Plugin, b.Data.Settings, b.Data.Granted = "", nil, nil
	}
	if old, err := s.d.Store.GetSource(b.ID); err != nil {
		return serverErr(c, err)
	} else if old != nil {
		return bad(c, "id exists")
	}
	if err := s.d.Store.SaveSource(b); err != nil {
		return serverErr(c, err)
	}
	s.d.Hub.SourcesChanged()
	return c.Status(fiber.StatusCreated).JSON(b)
}

func (s *Server) putSource(c fiber.Ctx) error {
	cur, err := s.d.Store.GetSource(c.Params("id"))
	if err != nil {
		return serverErr(c, err)
	}
	var b struct {
		Name string `json:"name"`
		Data *struct {
			URL      *string        `json:"url"`
			Proxy    *bool          `json:"proxy"`
			Settings map[string]any `json:"settings"` // только для плагин-виджетов: заменяет все настройки
			Granted  *[]string      `json:"granted"`  // только для плагин-виджетов: заменяет список выданных разрешений
		} `json:"data"`
	}
	if cur == nil || !body(c, &b) {
		return notFound(c)
	}
	if b.Name != "" {
		cur.Name = b.Name
	}
	if b.Data != nil {
		if b.Data.URL != nil {
			if !model.SafeURL(*b.Data.URL) {
				return bad(c, "invalid url")
			}
			cur.Data.URL = *b.Data.URL
		}
		if b.Data.Proxy != nil {
			cur.Data.Proxy = *b.Data.Proxy
		}
		if b.Data.Settings != nil {
			p, ok := s.d.Plugins.Get(cur.Data.Plugin)
			if cur.Type != "plugin" || !ok {
				return bad(c, "source is not a known plugin widget")
			}
			settings, err := p.CleanSettings(b.Data.Settings)
			if err != nil {
				return bad(c, "invalid settings: "+err.Error())
			}
			cur.Data.Settings = settings
		}
		if b.Data.Granted != nil {
			p, ok := s.d.Plugins.Get(cur.Data.Plugin)
			if cur.Type != "plugin" || !ok {
				return bad(c, "source is not a known plugin widget")
			}
			granted, err := p.CleanGranted(*b.Data.Granted)
			if err != nil {
				return bad(c, err.Error())
			}
			cur.Data.Granted = granted
		}
	}
	if cur.Data.Proxy && !model.IsAbsHTTP(cur.Data.URL) {
		return bad(c, "proxy needs an absolute http(s) URL")
	}
	if err := s.d.Store.SaveSource(*cur); err != nil {
		return serverErr(c, err)
	}
	s.d.Hub.SourcesChanged()
	return c.JSON(cur)
}

func (s *Server) deleteSource(c fiber.Ctx) error {
	ok, err := s.d.Store.DeleteSource(c.Params("id")) // чистит и висячие объекты во всех layout
	if err != nil {
		return serverErr(c, err)
	}
	if !ok {
		return notFound(c)
	}
	s.d.Hub.SourcesChanged()
	return c.JSON(fiber.Map{"ok": true})
}

// ---- /api/app ----

// appGuard: изменяющие действия требуют своего заголовка — обычная чужая страница в браузере не сможет отправить
// такой запрос (CORS-preflight не пройдёт), и «закрыть» приложение случайным fetch с другого сайта нельзя.
func (s *Server) appGuard(c fiber.Ctx) error {
	if c.Get("X-Overlay-Composer") != "1" {
		return bad(c, "missing x-overlay-composer header")
	}
	return c.Next()
}

func (s *Server) appInfo(c fiber.Ctx) error {
	a := s.d.App
	var autostart *bool
	if a.Autostart != nil {
		autostart = a.Autostart()
	}
	var upd any
	if a.Update != nil {
		upd = a.Update()
	}
	osName := a.OS
	if osName == "" {
		osName = runtime.GOOS
	}
	return c.JSON(fiber.Map{
		"compiled":      true,
		"os":            osName,
		"configFile":    s.d.Paths.ConfigFile,
		"dataDir":       s.d.Paths.DataDir,
		"pluginsDir":    s.d.Paths.PluginsDir,
		"logFile":       s.d.Paths.LogFile,
		"autostart":     autostart, // null — не поддерживается
		"tray":          a.TrayRunning != nil && a.TrayRunning(),
		"trayAvailable": a.TrayAvailable,
		"version":       a.Version,
		"update":        upd,
	})
}

func (s *Server) appAutostart(c fiber.Ctx) error {
	var b struct {
		Enabled *bool `json:"enabled"`
	}
	if !body(c, &b) || b.Enabled == nil {
		return bad(c, "enabled: boolean required")
	}
	if s.d.App.SetAutostart == nil {
		return bad(c, "autostart is not supported")
	}
	if err := s.d.App.SetAutostart(*b.Enabled); err != nil {
		slog.Error("autostart", "err", err)
		return bad(c, "autostart failed (see log)")
	}
	var now *bool
	if s.d.App.Autostart != nil {
		now = s.d.App.Autostart()
	}
	return c.JSON(fiber.Map{"autostart": now})
}

func (s *Server) appOpen(c fiber.Ctx) error {
	var b struct {
		What string `json:"what"`
	}
	if !body(c, &b) || (b.What != "data" && b.What != "logs" && b.What != "plugins") {
		return bad(c, "what: data | logs | plugins")
	}
	if s.d.App.Open != nil {
		if err := s.d.App.Open(b.What); err != nil {
			slog.Warn("open", "what", b.What, "err", err)
		}
	}
	return c.JSON(fiber.Map{"ok": true})
}

func (s *Server) appQuit(c fiber.Ctx) error {
	if s.d.App.Quit != nil {
		go s.d.App.Quit() // ответ успеет уйти раньше остановки сервера
	}
	return c.JSON(fiber.Map{"ok": true})
}

// POST /api/client-log — overlay (в OBS нет удобных DevTools) сообщает о старте и ошибках; пишем в лог приложения.
func (s *Server) clientLog(c fiber.Ctx) error {
	var b struct {
		Level string `json:"level"`
		Msg   string `json:"msg"`
		UA    string `json:"ua"`
	}
	if !body(c, &b) {
		return bad(c, "invalid json")
	}
	if !s.clientLogAllowed() {
		return c.JSON(fiber.Map{"ok": false})
	}
	if len(b.Msg) > 1000 {
		b.Msg = b.Msg[:1000]
	}
	if len(b.UA) > 200 {
		b.UA = b.UA[:200]
	}
	if b.Level == "error" {
		slog.Error("client", "msg", b.Msg, "ua", b.UA, "ip", c.IP())
	} else {
		slog.Info("client", "msg", b.Msg, "ua", b.UA, "ip", c.IP())
	}
	return c.JSON(fiber.Map{"ok": true})
}

// clientLogAllowed — не больше 120 записей в минуту, чтобы зациклившаяся страница не раздула лог.
func (s *Server) clientLogAllowed() bool {
	s.logMu.Lock()
	defer s.logMu.Unlock()
	now := time.Now()
	if now.Sub(s.logWindow) > time.Minute {
		s.logWindow, s.logCount = now, 0
	}
	s.logCount++
	return s.logCount <= 120
}
