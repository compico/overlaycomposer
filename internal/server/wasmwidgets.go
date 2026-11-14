package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"

	"github.com/compico/overlaycomposer/internal/hub"
	"github.com/compico/overlaycomposer/internal/plugins"
	"github.com/compico/overlaycomposer/internal/wasmplugins"
)

// Виджеты автономных плагинов. В списке «Add Source» они выглядят как обычные плагины-виджеты (Manifest с Widget и Wasm),
// источник хранит id плагина и id виджета, а iframe открывает /plugins/<id>/widget/<виджет>: это редирект на HTML из
// статики плагина (/plugins/<id>/ui/<entry>), query с настройками переносится.

// wasmManifests — виджеты загруженных wasm-плагинов в виде манифестов.
func (s *Server) wasmManifests() []plugins.Manifest {
	if s.d.Wasm == nil {
		return nil
	}
	infos, _ := s.d.Wasm.List()
	var out []plugins.Manifest
	for _, in := range infos {
		for _, w := range in.Meta.Widgets {
			out = append(out, widgetManifest(in.Meta, w))
		}
	}
	return out
}

func widgetManifest(m wasmplugins.Meta, w wasmplugins.Widget) plugins.Manifest {
	return plugins.Manifest{
		ID: m.ID, Name: w.Name, Version: m.Version, Description: m.Description, Author: m.Author,
		Entry: w.Entry, DefaultSize: plugins.Size{Width: w.Width, Height: w.Height},
		Widget: w.ID, Wasm: true,
	}
}

// widgetOf ищет виджет автономного плагина.
func (s *Server) widgetOf(plugin, widget string) (*plugins.Manifest, bool) {
	for _, m := range s.wasmManifests() {
		if m.ID == plugin && m.Widget == widget {
			return &m, true
		}
	}
	return nil, false
}

// sourceManifest — манифест для проверки настроек и разрешений источника-плагина: виджет автономного плагина
// (если указан widget) или обычный HTML-плагин.
func (s *Server) sourceManifest(plugin, widget string) (*plugins.Manifest, bool) {
	if widget != "" {
		return s.widgetOf(plugin, widget)
	}
	if s.d.Plugins == nil {
		return nil, false
	}
	p, ok := s.d.Plugins.Get(plugin)
	if !ok {
		return nil, false
	}
	return &p.Manifest, true
}

// wasmWidget: /plugins/<id>/widget/<виджет> → /plugins/<id>/ui/<entry>?<query>. Относительные адреса внутри HTML (app.js)
// должны разрешаться от /ui/, поэтому именно редирект, а не отдача файла по этому адресу.
func (s *Server) wasmWidget(c fiber.Ctx) error {
	id := c.Params("id")
	if s.d.Wasm == nil || !s.d.Wasm.Has(id) {
		return c.Next()
	}
	m, ok := s.widgetOf(id, c.Params("wid"))
	if !ok {
		return notFound(c)
	}
	parts := strings.Split(m.Entry, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	target := "/plugins/" + url.PathEscape(id) + "/ui/" + strings.Join(parts, "/")
	if u := c.OriginalURL(); strings.Contains(u, "?") {
		target += u[strings.Index(u, "?"):]
	}
	return c.Redirect().Status(fiber.StatusFound).To(target)
}

// hasSettingsPage — загружен ли плагин и объявляет ли он страницу настроек.
func (s *Server) hasSettingsPage(plugin string) bool {
	infos, _ := s.d.Wasm.List()
	for _, in := range infos {
		if in.Meta.ID == plugin && in.Meta.Settings != "" {
			return true
		}
	}
	return false
}

// ---- вызов плагина из его виджета или страницы настроек (ws plugin:call) ----

type pluginCallReq struct {
	Type   string          `json:"type"`
	ReqID  int             `json:"reqId"`
	Plugin string          `json:"plugin"`
	Source string          `json:"source"`
	Method string          `json:"method"`
	Path   string          `json:"path"`
	Query  map[string]any  `json:"query"`
	Body   json.RawMessage `json:"body"`
}

type pluginCallRes struct {
	Type   string          `json:"type"`
	ReqID  int             `json:"reqId"`
	OK     bool            `json:"ok"`
	Status int             `json:"status,omitempty"`
	Body   json.RawMessage `json:"body,omitempty"`
	Error  string          `json:"error,omitempty"`
}

const (
	maxPluginCallBody = 1 << 20
	maxPluginCallPath = 256
)

// pluginCallSlots — не больше 8 одновременных вызовов плагинов из виджетов на всё приложение: вызовы плагина и так
// выполняются по одному, а очередь из зависших страниц ничего не даёт.
var pluginCallSlots = make(chan struct{}, 8)

// authorizePlugin решает, может ли страница обращаться к плагину: без источника это страница настроек плагина (он должен
// её объявлять), иначе источник должен существовать и быть виджетом именно этого загруженного плагина. Пустая строка — можно,
// иначе текст отказа. Общая проверка для вызовов (plugin:call) и подписок (plugin:sub).
func (s *Server) authorizePlugin(plugin, source string) string {
	if s.d.Wasm == nil || s.d.Store == nil {
		return "not available"
	}
	if source == "" {
		// Страница настроек плагина (окно «Plugins → Settings»): источника нет, но плагин должен её объявлять.
		if plugin == "" || !s.hasSettingsPage(plugin) {
			return "unknown settings page"
		}
		return ""
	}
	src, err := s.d.Store.GetSource(source)
	if err != nil || src == nil || src.Type != "plugin" || src.Data.Plugin != plugin || src.Data.Widget == "" || plugin == "" {
		return "unknown widget"
	}
	if _, ok := s.widgetOf(src.Data.Plugin, src.Data.Widget); !ok {
		return "plugin is not loaded"
	}
	return ""
}

// pluginCall проверяет вызов и выполняет маршрут плагина. Страница лишь называет плагин и источник, решает сервер:
// источник должен существовать, быть виджетом именно этого автономного плагина, а маршрут — объявлен плагином.
func (s *Server) pluginCall(ctx context.Context, m pluginCallReq) pluginCallRes {
	res := pluginCallRes{Type: "plugin:result", ReqID: m.ReqID}
	fail := func(msg string) pluginCallRes { res.Error = msg; return res }
	if msg := s.authorizePlugin(m.Plugin, m.Source); msg != "" {
		return fail(msg)
	}
	switch m.Method {
	case "GET", "POST", "PUT", "PATCH", "DELETE":
	default:
		return fail("invalid method")
	}
	if m.Path == "" || len(m.Path) > maxPluginCallPath || !strings.HasPrefix(m.Path, "/") || strings.Contains(m.Path, "?") {
		return fail("invalid path")
	}
	if len(m.Body) > maxPluginCallBody {
		return fail("body is too large")
	}
	query := make(map[string]string, len(m.Query))
	for k, v := range m.Query {
		switch t := v.(type) {
		case string:
			query[k] = t
		case float64, bool:
			b, _ := json.Marshal(t)
			query[k] = string(b)
		default:
			return fail("invalid query")
		}
	}
	select {
	case pluginCallSlots <- struct{}{}:
		defer func() { <-pluginCallSlots }()
	default:
		return fail("busy")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	r, err := s.d.Wasm.Handle(ctx, m.Plugin, wasmplugins.Request{Method: m.Method, Path: m.Path, Query: query, Body: m.Body})
	if err != nil {
		switch {
		case errors.Is(err, wasmplugins.ErrNoRoute):
			return fail("no such route")
		case errors.Is(err, wasmplugins.ErrUnavailable):
			return fail("plugin is not running")
		}
		slog.Warn("wasm plugin: ошибка обработчика (вызов из виджета)", "plugin", m.Plugin, "path", m.Path, "err", err)
		return fail("plugin error")
	}
	res.OK, res.Status, res.Body = true, r.Status, r.Body
	return res
}

func (s *Server) pluginWS() {
	s.d.Hub.Handle("plugin:", func(p *hub.Peer, raw []byte) {
		var m pluginCallReq
		if json.Unmarshal(raw, &m) != nil {
			return
		}
		switch m.Type {
		case "plugin:sub", "plugin:unsub":
			s.pluginSubscription(p, raw)
			return
		case "plugin:call":
		default:
			return
		}
		go func() { p.Send(s.pluginCall(context.Background(), m)) }() // плагин может отвечать долго — чтение сообщений клиента не блокируем
	})
}
