// Package server — HTTP + WebSocket сервер на Fiber v3: REST API, /ws, встроенные страницы и бандлы клиента.
package server

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"mime"
	"net"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/contrib/v3/websocket"
	"github.com/gofiber/fiber/v3"

	"github.com/compico/overlaycomposer/internal/config"
	"github.com/compico/overlaycomposer/internal/hub"
	"github.com/compico/overlaycomposer/internal/obs"
	"github.com/compico/overlaycomposer/internal/paths"
	"github.com/compico/overlaycomposer/internal/proxy"
	"github.com/compico/overlaycomposer/internal/store"
	"github.com/compico/overlaycomposer/web"
)

// App — то, что серверу нужно от приложения (трей, автозапуск, выход). Любое поле может быть nil.
type App struct {
	OS            string
	Autostart     func() *bool // nil — не поддерживается
	SetAutostart  func(enabled bool) error
	Open          func(what string) error // "data" | "logs"
	Quit          func()
	TrayRunning   func() bool
	TrayAvailable bool
}

type Deps struct {
	Paths   paths.Paths
	Cfg     *config.Store
	Running config.Config // host/port, с которыми сервер реально запущен
	Store   *store.Store
	Hub     *hub.Hub
	Obs     *obs.Client
	Proxy   *proxy.Proxy
	App     App
}

type Server struct {
	d   Deps
	app *fiber.App

	logMu     sync.Mutex // ограничение частоты /api/client-log
	logWindow time.Time
	logCount  int
}

func New(d Deps) *Server {
	s := &Server{d: d}
	s.app = fiber.New(fiber.Config{
		AppName:   "Overlay Composer",
		BodyLimit: 8 << 20,
		ErrorHandler: func(c fiber.Ctx, err error) error {
			code := fiber.StatusInternalServerError
			if fe, ok := errors.AsType[*fiber.Error](err); ok {
				code = fe.Code
			} else {
				slog.Error("http", "path", c.Path(), "err", err)
			}
			return c.Status(code).JSON(fiber.Map{"error": strings.ToLower(httpText(code))})
		},
	})
	s.routes()
	return s
}

func httpText(code int) string {
	if code == fiber.StatusInternalServerError {
		return "internal error"
	}
	return fiber.NewError(code).Message
}

// OverlayURL — адрес для OBS Browser Source (0.0.0.0 / :: показываем как 127.0.0.1).
func (s *Server) OverlayURL() string { return s.baseURL() + "/overlay" }
func (s *Server) EditorURL() string  { return s.baseURL() + "/editor" }

func (s *Server) baseURL() string {
	h := s.d.Running.Host
	if h == "0.0.0.0" || h == "::" || h == "" {
		h = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(h, strconv.Itoa(s.d.Running.Port))
}

func (s *Server) routes() {
	app := s.app
	// 0. журнал доступа к страницам и бандлам: видно, дошёл ли OBS до /overlay, /dist/* и /ws
	app.Use(s.accessLog)

	// 1. прокси (маршруты /proxy*, затем запросы со страниц, открытых через прокси)
	s.d.Proxy.Register(app)

	// 2. WebSocket
	app.Get("/ws", websocket.New(s.serveWS))

	// 3. API и страницы
	s.api()
	app.Get("/", func(c fiber.Ctx) error { return c.Redirect().To("/editor") })
	app.Get("/editor", s.page(web.EditorHTML))
	app.Get("/overlay", s.page(web.OverlayHTML))
	app.Get("/widgets/clock", func(c fiber.Ctx) error { return s.htmlString(c, clockWidget) })
	app.Get("/widgets/box", func(c fiber.Ctx) error { return s.htmlString(c, boxWidget) })
	app.Get("/dist/*", s.dist)
}

// accessLog пишет в лог запросы к страницам, бандлам и /ws (с User-Agent: так видно, что это именно OBS/CEF).
func (s *Server) accessLog(c fiber.Ctx) error {
	p := c.Path()
	known := p == "/overlay" || p == "/editor" || p == "/ws" || strings.HasPrefix(p, "/dist/")
	err := c.Next()
	status := c.Response().StatusCode()
	switch {
	case known:
		slog.Info("http", "path", p, "status", status, "ua", shorten(c.Get("User-Agent"), 120))
	case status == fiber.StatusNotFound && !strings.HasPrefix(p, "/api/"):
		// Сюда попадают запросы страниц из iframe, которые не удалось привязать к источнику прокси
		// (нет Referer или он не наш) — по referer видно, почему.
		slog.Warn("http: 404", "path", p, "referer", shorten(c.Get("Referer"), 160), "ua", shorten(c.Get("User-Agent"), 60))
	}
	return err
}

func shorten(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

func (s *Server) page(load func() ([]byte, error)) fiber.Handler {
	return func(c fiber.Ctx) error {
		b, err := load()
		if err != nil {
			return fiber.ErrNotFound
		}
		c.Set("Content-Type", "text/html; charset=utf-8")
		c.Set("Cache-Control", "no-store")
		return c.Send(b)
	}
}

func (s *Server) htmlString(c fiber.Ctx, body string) error {
	c.Set("Content-Type", "text/html; charset=utf-8")
	return c.SendString(body)
}

// dist отдаёт файлы из встроенного каталога dist (path.Clean + fs.ValidPath защищают от выхода за пределы).
func (s *Server) dist(c fiber.Ctx) error {
	rel := path.Clean(strings.TrimPrefix(c.Params("*"), "/"))
	if !fs.ValidPath(rel) || rel == "." {
		return fiber.ErrNotFound
	}
	b, err := fs.ReadFile(web.Dist(), rel)
	if err != nil {
		return fiber.ErrNotFound
	}
	ct := "application/octet-stream"
	switch path.Ext(rel) {
	case ".js":
		ct = "text/javascript; charset=utf-8"
	case ".map":
		ct = "application/json"
	default:
		if m := mime.TypeByExtension(path.Ext(rel)); m != "" {
			ct = m
		}
	}
	c.Set("Content-Type", ct)
	c.Set("Cache-Control", "no-store")
	return c.Send(b)
}

// ---- WebSocket ----

// wsConn — hub.Conn: сообщения кладутся в очередь, а пишет в сокет отдельная горутина.
type wsConn struct{ ch chan []byte }

func (w *wsConn) Send(b []byte) bool {
	select {
	case w.ch <- b:
		return true
	default:
		return false // клиент не успевает — сообщение теряем, но хаб не блокируем
	}
}

func (s *Server) serveWS(c *websocket.Conn) {
	wc := &wsConn{ch: make(chan []byte, 64)}
	done := make(chan struct{})
	go func() {
		for {
			select {
			case b := <-wc.ch:
				if err := c.WriteMessage(websocket.TextMessage, b); err != nil {
					_ = c.Close() // разбудит чтение
					return
				}
			case <-done:
				return
			}
		}
	}()
	p := s.d.Hub.AddPeer(wc)
	defer func() {
		close(done)
		s.d.Hub.RemovePeer(p)
	}()
	for {
		mt, msg, err := c.ReadMessage()
		if err != nil {
			return
		}
		if mt == websocket.TextMessage {
			s.d.Hub.HandleMessage(p, msg)
		}
	}
}

// ---- запуск ----

// Listen занимает адрес сразу (чтобы вызывающий мог отличить «порт занят» от других ошибок) и обслуживает его в фоне.
// Ошибка работы сервера после старта приходит в канал.
func (s *Server) Listen() (<-chan error, error) {
	ln, err := net.Listen("tcp", net.JoinHostPort(s.d.Running.Host, strconv.Itoa(s.d.Running.Port)))
	if err != nil {
		return nil, err
	}
	errc := make(chan error, 1)
	go func() {
		errc <- s.app.Listener(ln, fiber.ListenConfig{DisableStartupMessage: true})
	}()
	return errc, nil
}

func (s *Server) Shutdown() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = s.app.ShutdownWithContext(ctx)
}
