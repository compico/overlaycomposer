// Package ocplugin — SDK для автономных (standalone) wasm-плагинов Overlay Composer на Go.
//
// Он прячет ABI (alloc/dealloc, упаковку указателей, JSON-обёртки): плагин регистрирует маршруты и обработчики событий,
// а SDK собирает метаданные для oc_meta и вызывает нужный обработчик.
//
//	func init() {
//		p := ocplugin.New(ocplugin.Meta{ID: "hello", Name: "Hello", Version: "0.1.0", Permissions: []string{ocplugin.PermStorage}})
//		p.GET("/greet", func(c *ocplugin.Ctx) error {
//			return c.JSON(200, map[string]string{"hello": c.Query("name")})
//		})
//	}
//
//	func main() {} // обязателен для сборки reactor, остаётся пустым
//
// Сборка: GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o hello.wasm .
//
// Плагин пассивен: в wasip1 нет потоков, горутины идут только внутри вызова хоста. Фоновая работа — через таймеры
// (SetTimer + OnTimer) и сетевые запросы хоста (Fetch + OnHTTPResponse). Состояние храните в Store/Load, а не в переменных:
// экземпляр может быть пересоздан после сбоя.
package ocplugin

import (
	"encoding/json"
	"fmt"
	"runtime/debug"
	"strings"
)

// Разрешения (плагин запрашивает, пользователь выдаёт).
const (
	PermStorage = "storage"
	PermNetHTTP = "net.http"
	PermRedeem  = "twitch.redemption"
	PermTimer   = "timer"
	PermPublish = "events.publish"
	PermChat    = "chat.send" // писать в чат канала от имени бота
)

const (
	apiVersion        = 1
	eventRedemption   = "twitch.redemption"
	eventTimer        = "timer"
	eventHTTPResponse = "http.response"
)

// Meta — описание плагина для oc_meta. Маршруты и подписки SDK добавляет сам по зарегистрированным обработчикам.
type Meta struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Version     string   `json:"version"`
	Description string   `json:"description,omitempty"`
	Author      string   `json:"author,omitempty"`
	Permissions []string `json:"permissions,omitempty"`
	// ConfigSchema — описание настроек для формы в интерфейсе (хост его не толкует).
	ConfigSchema map[string]any `json:"configSchema,omitempty"`
	// Widgets — виджеты плагина: HTML лежит в статике (секция oc.assets, её добавляет утилита ocpack), Entry — путь к нему.
	Widgets []Widget `json:"widgets,omitempty"`
	// Settings — путь HTML-страницы настроек в статике (секция oc.assets). Редактор показывает её в окне «Plugins → Settings».
	Settings string `json:"settings,omitempty"`
	// Events — топики, которые плагин публикует через Publish. Публиковать можно только объявленные.
	Events []EventDecl `json:"events,omitempty"`
}

// EventDecl — объявление публикуемого топика: id (`a-z0-9._-`, до 64 символов), название и описание для редакторов.
type EventDecl struct {
	ID          string `json:"id"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
}

// Widget — виджет плагина, который пользователь добавляет на сцену. Width/Height — размер по умолчанию (0 — 400×300).
type Widget struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Entry  string `json:"entry"`
	Width  int    `json:"width,omitempty"`
	Height int    `json:"height,omitempty"`
}

// Handler обрабатывает запрос к маршруту. Возвращённая ошибка — это 500 без подробностей наружу (подробности — в лог плагина).
type Handler func(c *Ctx) error

type route struct {
	Method  string `json:"method"`
	Path    string `json:"path"`
	Handler string `json:"handler"`
}

// Redemption — погашение награды за баллы канала.
type Redemption struct {
	ID          string `json:"id"`
	RewardID    string `json:"rewardId"`
	RewardTitle string `json:"rewardTitle"`
	UserID      string `json:"userId"`
	UserLogin   string `json:"userLogin"`
	UserName    string `json:"userName"`
	Input       string `json:"input"` // текст зрителя: недоверенный
	Status      string `json:"status"`
	RedeemedAt  string `json:"redeemedAt"`
}

// HTTPResponse — ответ на Fetch. Error непустой, если запрос не удался (тогда Status == 0).
type HTTPResponse struct {
	ID     string `json:"id"`
	Status int    `json:"status"`
	Body   string `json:"body"`
	Error  string `json:"error"`
}

// Plugin — регистрация маршрутов и обработчиков. В одном .wasm один плагин.
type Plugin struct {
	meta     Meta
	routes   []route
	handlers map[string]Handler
	onInit   func() error
	onRedeem func(Redemption)
	timers   map[string]func()
	fetches  map[string]func(HTTPResponse)
	config   json.RawMessage
}

var current *Plugin

// New создаёт плагин и делает его текущим. Вызывать один раз, из init().
func New(m Meta) *Plugin {
	if current != nil {
		panic("ocplugin: New is called twice: one .wasm contains one plugin")
	}
	current = &Plugin{meta: m, handlers: map[string]Handler{}, timers: map[string]func(){}, fetches: map[string]func(HTTPResponse){}, config: json.RawMessage(`{}`)}
	return current
}

func (p *Plugin) handle(method, path string, h Handler) {
	if h == nil || !strings.HasPrefix(path, "/") {
		panic(fmt.Sprintf("ocplugin: route %s %q: path must start with '/' and handler must not be nil", method, path))
	}
	for _, r := range p.routes {
		if r.Method == method && r.Path == path {
			panic(fmt.Sprintf("ocplugin: route %s %s is registered twice", method, path))
		}
	}
	name := fmt.Sprintf("h%d", len(p.routes))
	p.routes = append(p.routes, route{Method: method, Path: path, Handler: name})
	p.handlers[name] = h
}

// Маршруты доступны как /plugins/<id>/api<path>. Обработчик должен отвечать сразу: долгая работа — через события.
func (p *Plugin) GET(path string, h Handler)    { p.handle("GET", path, h) }
func (p *Plugin) POST(path string, h Handler)   { p.handle("POST", path, h) }
func (p *Plugin) PUT(path string, h Handler)    { p.handle("PUT", path, h) }
func (p *Plugin) PATCH(path string, h Handler)  { p.handle("PATCH", path, h) }
func (p *Plugin) DELETE(path string, h Handler) { p.handle("DELETE", path, h) }

// OnInit вызывается при каждом запуске экземпляра (в том числе после сбоя): здесь просят таймеры.
// Ошибка останавливает запуск плагина, текст попадёт в его состояние.
func (p *Plugin) OnInit(f func() error) { p.onInit = f }

// OnRedemption принимает погашения наград. Нужны разрешение PermRedeem в Meta и его выдача пользователем.
func (p *Plugin) OnRedemption(f func(Redemption)) { p.onRedeem = f }

// OnTimer вызывается, когда сработал таймер с этим id (см. SetTimer). Нужно разрешение PermTimer.
func (p *Plugin) OnTimer(id string, f func()) { p.timers[id] = f }

// OnHTTPResponse принимает ответ на Fetch с этим id. Нужно разрешение PermNetHTTP.
func (p *Plugin) OnHTTPResponse(id string, f func(HTTPResponse)) { p.fetches[id] = f }

// Config разбирает настройки, которые пользователь задал для плагина (доступны с OnInit).
func (p *Plugin) Config(v any) error { return json.Unmarshal(p.config, v) }

type metaOut struct {
	Meta
	APIVersion    int      `json:"apiVersion"`
	Routes        []route  `json:"routes,omitempty"`
	Subscriptions []string `json:"subscriptions,omitempty"`
}

func (p *Plugin) metaJSON() []byte {
	out := metaOut{Meta: p.meta, APIVersion: apiVersion, Routes: p.routes}
	if p.onRedeem != nil {
		out.Subscriptions = []string{eventRedemption}
	}
	b, _ := json.Marshal(out)
	return b
}

// init вызывается из oc_init({apiVersion, config}).
func (p *Plugin) init(arg []byte) {
	var in struct {
		Config json.RawMessage `json:"config"`
	}
	if json.Unmarshal(arg, &in) == nil && len(in.Config) > 0 {
		p.config = in.Config
	}
	if p.onInit != nil {
		if err := p.onInit(); err != nil {
			panic("ocplugin: OnInit: " + err.Error()) // trap: хост покажет причину и пометит плагин как failed
		}
	}
}

// dispatch вызывается из oc_call: обработчик маршрута (h0, h1, …) или on_event. Возвращает JSON ответа (nil — пусто).
func (p *Plugin) dispatch(name string, arg []byte) []byte {
	if name == "on_event" {
		p.onEvent(arg)
		return nil
	}
	h := p.handlers[name]
	if h == nil {
		return respond(404, map[string]string{"error": "unknown handler"})
	}
	var req struct {
		Method string            `json:"method"`
		Path   string            `json:"path"`
		Query  map[string]string `json:"query"`
		Body   json.RawMessage   `json:"body"`
	}
	if err := json.Unmarshal(arg, &req); err != nil {
		return respond(400, map[string]string{"error": "bad request"})
	}
	c := &Ctx{Method: req.Method, Path: req.Path, query: req.Query}
	if b := strings.TrimSpace(string(req.Body)); b != "" && b != "null" {
		c.body = req.Body
	}
	if err := safely("route "+req.Method+" "+req.Path, func() error { return h(c) }); err != nil {
		Error("handler failed", "route", req.Method+" "+req.Path, "err", err.Error())
		return respond(500, map[string]string{"error": "internal error"})
	}
	if c.status == 0 {
		return respond(204, nil)
	}
	return respond(c.status, c.respBody)
}

func (p *Plugin) onEvent(arg []byte) {
	var ev struct {
		Type string          `json:"type"`
		Data json.RawMessage `json:"data"`
	}
	if json.Unmarshal(arg, &ev) != nil {
		return
	}
	run := func(f func()) {
		_ = safely("event "+ev.Type, func() error { f(); return nil })
	}
	switch ev.Type {
	case eventRedemption:
		var r Redemption
		if p.onRedeem != nil && json.Unmarshal(ev.Data, &r) == nil {
			run(func() { p.onRedeem(r) })
		}
	case eventTimer:
		var t struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(ev.Data, &t) == nil && p.timers[t.ID] != nil {
			run(p.timers[t.ID])
		}
	case eventHTTPResponse:
		var r HTTPResponse
		if json.Unmarshal(ev.Data, &r) == nil && p.fetches[r.ID] != nil {
			run(func() { p.fetches[r.ID](r) })
		}
	}
}

// safely вызывает f и превращает панику в ошибку (с трассировкой в лог): падение обработчика не должно ронять экземпляр.
func safely(what string, f func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			Error("panic", "in", what, "value", fmt.Sprint(r), "stack", string(debug.Stack()))
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return f()
}

func respond(status int, body any) []byte {
	var raw json.RawMessage
	switch b := body.(type) {
	case nil:
	case json.RawMessage:
		raw = b
	default:
		raw, _ = json.Marshal(b)
	}
	out, _ := json.Marshal(struct {
		Status int             `json:"status"`
		Body   json.RawMessage `json:"body,omitempty"`
	}{status, raw})
	return out
}
