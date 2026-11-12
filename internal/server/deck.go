package server

import (
	"encoding/json"
	"errors"
	"log/slog"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/gofiber/fiber/v3"

	"github.com/compico/overlaycomposer/internal/deck"
	"github.com/compico/overlaycomposer/internal/obs"
)

// Deck — панель управления для старых устройств (iPad/Safari 9). Страница /deck целиком собирается здесь (internal/deck);
// раскладка хранится в OC (таблица settings, ключ deck_layout), а нажатие несёт только id плитки: действие определяет сервер
// по сохранённой раскладке, поэтому устройство не может вызвать ничего, кроме того, что в ней записано.

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
	v := deck.ViewData{Layout: l, PageID: c.Query("page")}
	if o := s.deckObs(); o != nil {
		v.Online, v.Scene = o.Connected(), o.CurrentScene()
	}
	out, err := deck.Render(v)
	if err != nil {
		return err
	}
	c.Set("Cache-Control", "no-store")
	c.Set("Content-Security-Policy", deck.CSP)
	c.Set("X-Content-Type-Options", "nosniff")
	c.Set("Content-Type", "text/html; charset=utf-8")
	return c.Send(out)
}

func (s *Server) deckState(c fiber.Ctx) error {
	l, _ := s.deckLayout()
	st := fiber.Map{"rev": l.Rev, "online": false, "scene": ""}
	if o := s.deckObs(); o != nil {
		st["online"], st["scene"] = o.Connected(), o.CurrentScene()
	}
	c.Set("Cache-Control", "no-store")
	return c.JSON(st)
}

func (s *Server) deckPress(c fiber.Ctx) error {
	var b struct {
		Tile    string `json:"tile"`
		Confirm bool   `json:"confirm"`
	}
	if !body(c, &b) {
		return nil
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
	if t.Confirm && !b.Confirm {
		return c.Status(fiber.StatusConflict).JSON(fiber.Map{"error": "confirm required", "confirm": true})
	}
	switch t.Action.Type {
	case deck.ActionScene:
		o := s.deckObs()
		if o == nil || !o.Connected() {
			return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "OBS is not connected"})
		}
		if err := o.SetScene(t.Action.Scene); err != nil {
			if errors.Is(err, obs.ErrUnknownScene) {
				return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "scene not found in OBS"})
			}
			return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(fiber.Map{"ok": true})
	default:
		return bad(c, "this tile has no press action")
	}
}

func (s *Server) deckGetLayout(c fiber.Ctx) error {
	l, saved := s.deckLayout()
	var scenes []string
	if o := s.deckObs(); o != nil {
		scenes = o.Scenes()
	}
	colors, icons := make([]string, 0, len(deck.Palette)), make([]string, 0, len(deck.Icons))
	for n := range deck.Palette {
		colors = append(colors, n)
	}
	for n := range deck.Icons {
		icons = append(icons, n)
	}
	sort.Strings(colors)
	sort.Strings(icons)
	return c.JSON(fiber.Map{
		"layout": l, "saved": saved, "scenes": scenes, "colors": colors, "icons": icons,
		"grid": fiber.Map{"cols": deck.Cols, "rows": deck.Rows},
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
	return c.JSON(fiber.Map{"ok": true, "rev": cur.Rev + 1})
}
