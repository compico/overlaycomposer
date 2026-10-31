// Package server — HTTP + WebSocket сервер на Fiber v3: REST API, /ws, встроенные страницы и бандлы клиента.
package server

import (
	"bytes"
	"context"
	"errors"
	"io"
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

	"github.com/compico/overlaycomposer/internal/auth"
	"github.com/compico/overlaycomposer/internal/chat"
	"github.com/compico/overlaycomposer/internal/config"
	"github.com/compico/overlaycomposer/internal/hub"
	"github.com/compico/overlaycomposer/internal/kv"
	"github.com/compico/overlaycomposer/internal/market"
	"github.com/compico/overlaycomposer/internal/notify"
	"github.com/compico/overlaycomposer/internal/obs"
	"github.com/compico/overlaycomposer/internal/paths"
	"github.com/compico/overlaycomposer/internal/plugins"
	"github.com/compico/overlaycomposer/internal/proxy"
	"github.com/compico/overlaycomposer/internal/store"
	"github.com/compico/overlaycomposer/internal/twitch"
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
	Version       string
	Update        func() any // последний известный результат проверки обновлений (nil — проверки нет)
}

type Deps struct {
	Paths   paths.Paths
	Cfg     *config.Store
	Running config.Config // host/port, с которыми сервер реально запущен
	Store   *store.Store
	Hub     *hub.Hub
	Obs     *obs.Client
	Proxy   *proxy.Proxy
	Plugins *plugins.Registry
	Auth    *auth.Manager
	Twitch  *twitch.Manager
	Chat    *chat.Service
	KV      *kv.Service
	Market  *market.Market
	Notify  *notify.Center
	// CheckPlugins запускает проверку обновлений плагинов (force — по кнопке); nil — недоступно.
	CheckPlugins func(ctx context.Context, force bool) error
	App          App
}

type Server struct {
	d   Deps
	app *fiber.App

	logMu     sync.Mutex // ограничение частоты /api/client-log
	logWindow time.Time
	logCount  int

	deny *denyLimiter // отказы без токена по адресам (nil — без ограничения, как в тестах)

	// Сайты источников проксируются на отдельном порту: другой origin, и JS чужого сайта не достаёт до API приложения.
	// proxyLn == nil — изоляции нет (отключена в config.json или порт занят), прокси работает на основном порту.
	proxyLn   net.Listener
	proxyApp  *fiber.App
	proxyPort int
}

func appConfig() fiber.Config {
	return fiber.Config{
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
	}
}

func New(d Deps) *Server {
	s := &Server{d: d, deny: newDenyLimiter(120, time.Minute)}
	s.app = fiber.New(appConfig())
	s.openProxyListener()
	s.routes()
	s.chatWS()
	s.storageWS()
	s.netWS()
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
	// 0.1 запросы с чужих страниц (Origin ≠ Host) отклоняем; затем доступ с других устройств только по токену (с localhost — свободно)
	app.Use(s.originGuard)
	app.Use(s.authGuard)
	s.authRoutes()
	s.twitchRoutes()
	s.storageRoutes()
	s.marketRoutes()
	s.notifyRoutes()

	// 1. прокси (маршруты /proxy*, затем запросы со страниц, открытых через прокси). При изоляции он живёт на своём порту
	// (newProxyApp): на основном origin сайты источников не отдаются вообще.
	if s.proxyLn == nil {
		s.d.Proxy.Register(app)
	}

	// 2. WebSocket
	app.Get("/ws", websocket.New(s.serveWS))

	// 3. API и страницы
	s.api()
	app.Get("/", func(c fiber.Ctx) error { return c.Redirect().To("/editor") })
	app.Get("/editor", s.page(web.EditorHTML))
	app.Get("/overlay", s.page(web.OverlayHTML))
	app.Get("/dist/*", s.dist)
	app.Get("/plugin-sdk/:name", s.pluginSDK)
	app.Get("/plugins/:id/*", s.pluginFile)
	app.Get("/plugins/:id", func(c fiber.Ctx) error { return c.Redirect().To("/plugins/" + c.Params("id") + "/") })
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
		return c.Send(s.withProxyPort(b))
	}
}

// withProxyPort сообщает странице порт прокси (<meta name="oc-proxy-port">), если прокси изолирован на своём порту.
func (s *Server) withProxyPort(html []byte) []byte {
	if s.proxyPort == 0 {
		return html
	}
	tag := []byte(`<meta name="oc-proxy-port" content="` + strconv.Itoa(s.proxyPort) + `">`)
	i := bytes.Index(html, []byte("</head>"))
	if i < 0 {
		return html
	}
	out := make([]byte, 0, len(html)+len(tag))
	out = append(out, html[:i]...)
	out = append(out, tag...)
	return append(out, html[i:]...)
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

// pluginFile раздаёт файлы плагина-виджета: /plugins/<id>/<путь>. Каталог и пустой путь — точка входа плагина.
// Виджет работает в iframe с sandbox без allow-same-origin (у него «непрозрачный» origin), поэтому для его
// запросов к своим файлам нужен CORS; файлы плагина публичны, секретов в них нет.
func (s *Server) pluginFile(c fiber.Ctx) error {
	p, ok := s.d.Plugins.Get(c.Params("id"))
	if !ok {
		return fiber.ErrNotFound
	}
	f, name, err := p.Open(c.Params("*"))
	if err != nil {
		return fiber.ErrNotFound
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 64<<20))
	if err != nil {
		return fiber.ErrInternalServerError
	}
	ct := mime.TypeByExtension(path.Ext(name))
	if ct == "" {
		ct = "application/octet-stream"
	}
	c.Set("Content-Type", ct)
	c.Set("Cache-Control", "no-cache")
	c.Set("Access-Control-Allow-Origin", "*")
	c.Set("X-Content-Type-Options", "nosniff")
	return c.Send(b)
}

// pluginSDK раздаёт SDK для плагинов: oc.js (бандл) и oc.d.ts (типы). Публичные файлы, как и сами плагины.
func (s *Server) pluginSDK(c fiber.Ctx) error {
	var b []byte
	var err error
	ct := "text/javascript; charset=utf-8"
	switch c.Params("name") {
	case "oc.js":
		b, err = fs.ReadFile(web.Dist(), "oc.js")
	case "oc.d.ts":
		b, err = web.SDKTypes()
		ct = "text/plain; charset=utf-8"
	default:
		return fiber.ErrNotFound
	}
	if err != nil {
		return fiber.ErrNotFound
	}
	c.Set("Content-Type", ct)
	c.Set("Cache-Control", "no-cache")
	c.Set("Access-Control-Allow-Origin", "*")
	c.Set("X-Content-Type-Options", "nosniff")
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

// maxWSMessage — предел размера входящего WebSocket-сообщения (как BodyLimit у HTTP): без него один клиент мог бы
// заставить сервер прочитать в память сообщение любого размера.
const maxWSMessage = 8 << 20

func (s *Server) serveWS(c *websocket.Conn) {
	c.SetReadLimit(maxWSMessage)
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

// proxyPortFor — порт изолированного прокси по настройке (0 — изоляции нет).
func proxyPortFor(r config.Config) int {
	if r.Port <= 0 || r.ProxyPort < 0 {
		return 0
	}
	p := r.ProxyPort
	if p == 0 {
		p = r.Port + 1
	}
	if p > 65535 || p == r.Port {
		return 0
	}
	return p
}

// openProxyListener занимает порт прокси. Не вышло (порт занят) — не ошибка: прокси работает на основном порту, как раньше,
// но без изоляции; причина пишется в лог.
func (s *Server) openProxyListener() {
	p := proxyPortFor(s.d.Running)
	if p == 0 {
		return
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(s.d.Running.Host, strconv.Itoa(p)))
	if err != nil {
		slog.Warn("прокси: порт занят — сайты источников открываются на основном порту без изоляции (задайте proxyPort в config.json)", "port", p, "err", err)
		return
	}
	s.proxyLn, s.proxyPort = ln, p
	s.proxyApp = s.newProxyApp()
}

// newProxyApp — сервер прокси: только /proxy*, запросы со страниц, открытых через прокси, и защита (Origin, токен устройства).
// API, WebSocket и страницы приложения тут не существуют — любой другой путь отвечает 404.
func (s *Server) newProxyApp() *fiber.App {
	app := fiber.New(appConfig())
	app.Use(s.accessLog, s.originGuard, s.authGuard)
	s.d.Proxy.Register(app)
	app.Use(func(c fiber.Ctx) error { return notFound(c) })
	return app
}

// Listen занимает адрес сразу (чтобы вызывающий мог отличить «порт занят» от других ошибок) и обслуживает его в фоне.
// Ошибка работы сервера после старта приходит в канал.
func (s *Server) Listen() (<-chan error, error) {
	ln, err := net.Listen("tcp", net.JoinHostPort(s.d.Running.Host, strconv.Itoa(s.d.Running.Port)))
	if err != nil {
		if s.proxyLn != nil {
			_ = s.proxyLn.Close()
		}
		return nil, err
	}
	errc := make(chan error, 2)
	go func() {
		errc <- s.app.Listener(ln, fiber.ListenConfig{DisableStartupMessage: true})
	}()
	if s.proxyLn != nil {
		go func() {
			if err := s.proxyApp.Listener(s.proxyLn, fiber.ListenConfig{DisableStartupMessage: true}); err != nil {
				slog.Error("прокси: сервер остановлен", "err", err)
			}
		}()
		slog.Info("прокси изолирован на отдельном порту", "port", s.proxyPort)
	}
	return errc, nil
}

func (s *Server) Shutdown() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if s.proxyApp != nil {
		_ = s.proxyApp.ShutdownWithContext(ctx)
	}
	_ = s.app.ShutdownWithContext(ctx)
}
