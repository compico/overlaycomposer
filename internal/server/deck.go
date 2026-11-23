package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/gofiber/contrib/v3/websocket"
	"github.com/gofiber/fiber/v3"

	"github.com/compico/overlaycomposer/internal/deck"
	"github.com/compico/overlaycomposer/internal/obs"
	"github.com/compico/overlaycomposer/internal/wasmplugins"
)

// Deck — панель управления для старых устройств (iPad/Safari 9). Страница /deck целиком собирается здесь (internal/deck);
// раскладка хранится в OC (таблица settings, ключ deck_layout), а нажатие несёт только id плитки: действие определяет сервер
// по сохранённой раскладке, поэтому устройство не может вызвать ничего, кроме того, что в ней записано.

const deckPluginTimeout = 8 * time.Second

const (
	deckLayoutKey = "deck_layout"
	deckRevKey    = "deck_rev" // ревизия раскладки по умолчанию: растёт при сбросе, не возвращаясь назад
)

// deckOBS — то, что панели нужно от OBS (в тестах подменяется).
type deckOBS interface {
	Connected() bool
	CurrentScene() string
	Scenes() []string
	SetScene(name string) error
	AudioInputs() []string
	Muted(name string) bool
	ToggleMute(name string) error
	// Микшер (группа kind=mixer): уровни активных источников и команды им.
	WatchMixer(fn func([]obs.MixerInput)) (stop func())
	ToggleInputMute(name string) error
	ToggleInputMonitor(name string) error
	SetInputVolumeDB(name string, db float64) error
}

// deckPlugins — то, что панели нужно от wasm-плагинов (в тестах подменяется): список и вызов маршрута.
type deckPlugins interface {
	List() ([]wasmplugins.Info, []wasmplugins.Problem)
	Handle(ctx context.Context, id string, req wasmplugins.Request) (wasmplugins.Response, error)
}

func (s *Server) deckPlugins() deckPlugins {
	if s.deckPluginsOverride != nil {
		return s.deckPluginsOverride
	}
	if s.d.Wasm == nil {
		return nil
	}
	return s.d.Wasm
}

func (s *Server) deckObs() deckOBS {
	if s.deckObsOverride != nil {
		return s.deckObsOverride
	}
	if s.d.Obs == nil {
		return nil
	}
	return s.d.Obs
}

// deckBucket — общий предел частоты нажатий: зависший палец или сломанный клиент не завалит OBS запросами.
type deckBucket struct {
	mu     sync.Mutex
	tokens float64
	last   time.Time
}

const (
	deckBurst = 16
	deckRate  = 8 // нажатий в секунду
)

func (b *deckBucket) allow(now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.last.IsZero() {
		b.tokens = deckBurst
	} else {
		b.tokens += now.Sub(b.last).Seconds() * deckRate
		if b.tokens > deckBurst {
			b.tokens = deckBurst
		}
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func (s *Server) deckRoutes() {
	app := s.app
	app.Get("/deck", s.deckPage)
	app.Get("/deck/probe", s.deckProbe)
	app.Get("/api/deck/state", s.deckState)
	app.Get("/api/deck/ws", s.deckWSGuard, websocket.New(s.serveDeckWS))
	app.Post("/api/deck/press", s.appGuard, s.deckPress)
	app.Get("/api/deck/layout", s.deckGetLayout)
	app.Put("/api/deck/layout", s.appGuard, s.deckPutLayout)
	app.Delete("/api/deck/layout", s.appGuard, s.deckResetLayout)
}

// deckLayout отдаёт сохранённую раскладку, а если её нет или она не читается — раскладку по умолчанию (плитка на каждую сцену).
func (s *Server) deckLayout() (l deck.Layout, saved bool) {
	if raw, ok, err := s.d.Store.Setting(deckLayoutKey); err == nil && ok && raw != "" {
		if l, err = deck.Parse([]byte(raw)); err == nil {
			return l, true
		}
		slog.Warn("deck: saved layout is invalid, using the default", "err", err)
	}
	var scenes []string
	if o := s.deckObs(); o != nil {
		scenes = o.Scenes()
	}
	l = deck.Default(scenes)
	if raw, ok, err := s.d.Store.Setting(deckRevKey); err == nil && ok {
		l.Rev, _ = strconv.Atoi(raw)
	}
	return l, false
}

func (s *Server) deckPage(c fiber.Ctx) error {
	l, _ := s.deckLayout()
	v := deck.ViewData{Layout: l}
	if o := s.deckObs(); o != nil {
		v.Online, v.Scene = o.Connected(), o.CurrentScene()
		v.Muted = map[string]bool{}
		for _, g := range l.Groups {
			for _, t := range g.Tiles {
				if t.Action.Type == deck.ActionMute && o.Muted(t.Action.Input) {
					v.Muted[t.Action.Input] = true
				}
			}
		}
	}
	out, err := deck.Render(v)
	if err != nil {
		return err
	}
	c.Set("Cache-Control", "no-store")
	c.Set("Content-Security-Policy", deck.CSPFor(c.Hostname()))
	c.Set("X-Content-Type-Options", "nosniff")
	c.Set("Content-Type", "text/html; charset=utf-8")
	return c.Send(out)
}

func (s *Server) deckState(c fiber.Ctx) error {
	c.Set("Cache-Control", "no-store")
	return c.JSON(s.deckStateNow())
}

// deckSetScene переключает сцену OBS; имя уже проверено по сохранённой раскладке.
func (s *Server) deckSetScene(c fiber.Ctx, scene string) error {
	o := s.deckObs()
	if o == nil || !o.Connected() {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "OBS is not connected"})
	}
	if err := o.SetScene(scene); err != nil {
		if errors.Is(err, obs.ErrUnknownScene) {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "scene not found in OBS"})
		}
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(fiber.Map{"ok": true})
}

// deckToggleMute переключает mute источника звука; имя уже проверено по сохранённой раскладке.
func (s *Server) deckToggleMute(c fiber.Ctx, input string) error {
	o := s.deckObs()
	if o == nil || !o.Connected() {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "OBS is not connected"})
	}
	if err := o.ToggleMute(input); err != nil {
		if errors.Is(err, obs.ErrUnknownInput) {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "audio input not found in OBS"})
		}
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(fiber.Map{"ok": true})
}

func (s *Server) deckPress(c fiber.Ctx) error {
	var b struct {
		Tile    string `json:"tile"`
		Confirm bool   `json:"confirm"`
		Scene   string `json:"scene"` // для папки: выбранная сцена из её списка
	}
	if !body(c, &b) {
		return bad(c, "invalid json body")
	}
	if !s.deckRate.allow(time.Now()) {
		c.Set("Retry-After", "1")
		return c.Status(fiber.StatusTooManyRequests).JSON(fiber.Map{"error": "too many presses"})
	}
	l, _ := s.deckLayout()
	t, ok := l.Tile(b.Tile)
	if !ok {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "no such tile"})
	}
	if t.Confirm && !b.Confirm && t.Action.Type != deck.ActionFolder {
		return c.Status(fiber.StatusConflict).JSON(fiber.Map{"error": "confirm required", "confirm": true})
	}
	switch t.Action.Type {
	case deck.ActionScene:
		return s.deckSetScene(c, t.Action.Scene)
	case deck.ActionFolder:
		if b.Scene == "" || !t.Action.HasScene(b.Scene) {
			return bad(c, "pick a scene from this folder")
		}
		return s.deckSetScene(c, b.Scene)
	case deck.ActionPlugin:
		return s.deckPluginPress(c, t)
	case deck.ActionMute:
		return s.deckToggleMute(c, t.Action.Input)
	default:
		return bad(c, "this tile has no press action")
	}
}

// deckPluginPress вызывает действие плагина. Маршрут и тело берутся из Meta плагина (его объявление), а не от устройства:
// плитка называет только плагин и действие.
func (s *Server) deckPluginPress(c fiber.Ctx, t deck.Tile) error {
	pl := s.deckPlugins()
	if pl == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "plugins are disabled"})
	}
	infos, _ := pl.List()
	var info *wasmplugins.Info
	for i := range infos {
		if infos[i].Meta.ID == t.Action.Plugin {
			info = &infos[i]
		}
	}
	if info == nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "plugin is not installed"})
	}
	a, ok := info.Meta.FindAction(t.Action.PluginAction)
	if !ok {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "the plugin has no such action"})
	}
	if info.State != wasmplugins.StateRunning {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "plugin is not running"})
	}
	ctx, cancel := context.WithTimeout(c.Context(), deckPluginTimeout)
	defer cancel()
	res, err := pl.Handle(ctx, info.Meta.ID, wasmplugins.Request{Method: a.Method, Path: a.Path, Body: a.Body})
	if err != nil {
		slog.Warn("deck: действие плагина не выполнено", "plugin", info.Meta.ID, "action", a.ID, "err", err)
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{"error": "the plugin did not answer"})
	}
	if res.Status >= 400 {
		// Ответ плагина («ничего не играет») полезен на плитке, но не больше короткой строки.
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(res.Body, &e)
		msg := e.Error
		if r := []rune(msg); len(r) > 120 {
			msg = string(r[:120])
		}
		if msg == "" {
			msg = "the plugin refused the action"
		}
		code := fiber.StatusConflict
		if res.Status >= 500 {
			code = fiber.StatusBadGateway
		}
		return c.Status(code).JSON(fiber.Map{"error": msg})
	}
	return c.JSON(fiber.Map{"ok": true})
}

// deckPluginActions — действия установленных плагинов для выбора в редакторе плитки.
func (s *Server) deckPluginActions() []fiber.Map {
	out := []fiber.Map{}
	pl := s.deckPlugins()
	if pl == nil {
		return out
	}
	infos, _ := pl.List()
	for _, in := range infos {
		for _, a := range in.Meta.Actions {
			out = append(out, fiber.Map{"plugin": in.Meta.ID, "pluginName": in.Meta.Name, "id": a.ID, "title": a.Title, "icon": a.Icon, "danger": a.Danger})
		}
	}
	return out
}

func (s *Server) deckGetLayout(c fiber.Ctx) error {
	l, saved := s.deckLayout()
	var scenes []string
	inputs := []string{}
	if o := s.deckObs(); o != nil {
		scenes = o.Scenes()
		inputs = append(inputs, o.AudioInputs()...)
	}
	return c.JSON(fiber.Map{
		"layout": l, "saved": saved, "scenes": scenes, "inputs": inputs, "palette": deck.Palette, "icons": deck.Icons, "pluginActions": s.deckPluginActions(),
		"grid": fiber.Map{"rows": deck.Rows, "maxGroupCols": deck.MaxGroupCols, "sizes": deck.Sizes},
	})
}

// deckPutLayout сохраняет раскладку. rev в теле должен совпасть с текущим (два редактора не затрут друг друга); новый rev ставит сервер.
func (s *Server) deckPutLayout(c fiber.Ctx) error {
	in, err := deck.Parse(c.Body())
	if err != nil {
		return bad(c, err.Error())
	}
	s.deckMu.Lock()
	defer s.deckMu.Unlock()
	cur, _ := s.deckLayout()
	if in.Rev != cur.Rev {
		return c.Status(fiber.StatusConflict).JSON(fiber.Map{"error": "the layout was changed elsewhere", "rev": cur.Rev})
	}
	in.Rev = cur.Rev + 1
	raw, err := json.Marshal(in)
	if err != nil {
		return err
	}
	if err := s.d.Store.SetSetting(deckLayoutKey, string(raw)); err != nil {
		return err
	}
	s.deckPoke()
	return c.JSON(fiber.Map{"ok": true, "rev": in.Rev})
}

// deckResetLayout возвращает раскладку по умолчанию. Счётчик ревизий не откатывается (deck_rev): иначе устройство, которое помнит
// старую ревизию с таким же номером, не заметило бы нового сохранения.
func (s *Server) deckResetLayout(c fiber.Ctx) error {
	s.deckMu.Lock()
	defer s.deckMu.Unlock()
	cur, saved := s.deckLayout()
	if !saved {
		return c.JSON(fiber.Map{"ok": true, "rev": cur.Rev})
	}
	if err := s.d.Store.SetSetting(deckRevKey, strconv.Itoa(cur.Rev+1)); err != nil {
		return err
	}
	if err := s.d.Store.SetSetting(deckLayoutKey, ""); err != nil {
		return err
	}
	s.deckPoke()
	return c.JSON(fiber.Map{"ok": true, "rev": cur.Rev + 1})
}
