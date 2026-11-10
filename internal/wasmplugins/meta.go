// Package wasmplugins — автономные (standalone) плагины на WebAssembly.
//
// Плагин — один .wasm-файл на любом языке (Rust, Go, …), который реализует небольшой JSON-ABI (см. docs/WASM_PLUGINS.md).
// Манифеста нет: метаданные плагин отдаёт сам через экспорт oc_meta. Плагин пассивен: он отвечает на маршруты и события,
// а долгоживущими ресурсами (таймеры, сетевые запросы, EventSub) владеет хост.
package wasmplugins

import (
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/compico/overlaycomposer/internal/wasmassets"
)

// APIVersion — версия ABI между хостом и плагином.
const APIVersion = 1

// Разрешения плагина. Плагин их запрашивает в oc_meta, пользователь выдаёт; без выдачи операция отклоняется.
const (
	PermStorage  = "storage"
	PermNetHTTP  = "net.http"
	PermRedeem   = "twitch.redemption"
	PermTimer    = "timer"
	maxRoutes    = 64
	maxMetaBytes = 64 << 10
	maxWidgets   = 16
	defWidgetW   = 400
	defWidgetH   = 300
	maxWidgetDim = 8192
)

// События, на которые плагин может подписаться.
const (
	EventRedemption   = "twitch.redemption"
	EventTimer        = "timer"
	EventHTTPResponse = "http.response"
)

var (
	idRe      = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
	semverRe  = regexp.MustCompile(`^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`)
	handlerRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)
	pathSegRe = regexp.MustCompile(`^[A-Za-z0-9._~-]+$`)
)

var knownPerms = map[string]bool{PermStorage: true, PermNetHTTP: true, PermRedeem: true, PermTimer: true}

var knownEvents = map[string]bool{EventRedemption: true, EventTimer: true, EventHTTPResponse: true}

// Route — HTTP-маршрут плагина: он доступен как /plugins/<id>/api<Path>.
type Route struct {
	Method  string `json:"method"`
	Path    string `json:"path"`
	Handler string `json:"handler"`
}

// Meta — то, что плагин сообщает о себе в oc_meta.
type Meta struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Version       string   `json:"version"`
	Description   string   `json:"description,omitempty"`
	Author        string   `json:"author,omitempty"`
	APIVersion    int      `json:"apiVersion"`
	Permissions   []string `json:"permissions,omitempty"`
	Routes        []Route  `json:"routes,omitempty"`
	Subscriptions []string `json:"subscriptions,omitempty"`
	// ConfigSchema — необязательное описание настроек (JSON Schema-подобное), UI строит по нему форму. Хост его не толкует.
	ConfigSchema map[string]any `json:"configSchema,omitempty"`
	// Widgets — виджеты плагина (iframe с HTML из секции oc.assets); пользователь добавляет их на сцену как обычные виджеты.
	Widgets []Widget `json:"widgets,omitempty"`
	// Settings — страница настроек плагина: путь HTML-файла в статике (/plugins/<id>/ui/<settings>). Редактор открывает её
	// в sandbox-iframe в окне «Plugins → Settings»; страница говорит только с маршрутами своего плагина (oc.plugin.*).
	Settings string `json:"settings,omitempty"`
}

// Widget — виджет плагина. Entry — путь HTML-файла в статике плагина (/plugins/<id>/ui/<entry>).
type Widget struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Entry  string `json:"entry"`
	Width  int    `json:"width,omitempty"`
	Height int    `json:"height,omitempty"`
}

// Validate проверяет метаданные, чтобы кривой плагин не попал в маршруты и интерфейс.
func (m *Meta) Validate() error {
	if !idRe.MatchString(m.ID) {
		return fmt.Errorf("id %q: a-z, 0-9 and '-', up to 64 characters", m.ID)
	}
	if strings.TrimSpace(m.Name) == "" || len([]rune(m.Name)) > 80 {
		return fmt.Errorf("name: 1..80 characters")
	}
	if !semverRe.MatchString(m.Version) {
		return fmt.Errorf("version %q: semver expected (1.2.3)", m.Version)
	}
	if m.APIVersion != APIVersion {
		return fmt.Errorf("apiVersion %d is not supported (host speaks %d)", m.APIVersion, APIVersion)
	}
	perms := map[string]bool{}
	for _, p := range m.Permissions {
		if !knownPerms[p] {
			return fmt.Errorf("unknown permission %q", p)
		}
		if perms[p] {
			return fmt.Errorf("duplicate permission %q", p)
		}
		perms[p] = true
	}
	if len(m.Routes) > maxRoutes {
		return fmt.Errorf("too many routes (max %d)", maxRoutes)
	}
	seen := map[string]bool{}
	for _, r := range m.Routes {
		switch r.Method {
		case "GET", "POST", "PUT", "PATCH", "DELETE":
		default:
			return fmt.Errorf("route %s %s: method not allowed", r.Method, r.Path)
		}
		if err := validRoutePath(r.Path); err != nil {
			return fmt.Errorf("route %s %s: %w", r.Method, r.Path, err)
		}
		if !handlerRe.MatchString(r.Handler) || r.Handler == "on_event" {
			return fmt.Errorf("route %s %s: bad handler name %q", r.Method, r.Path, r.Handler)
		}
		k := r.Method + " " + r.Path
		if seen[k] {
			return fmt.Errorf("duplicate route %s", k)
		}
		seen[k] = true
	}
	subs := map[string]bool{}
	for _, s := range m.Subscriptions {
		if !knownEvents[s] {
			return fmt.Errorf("unknown subscription %q", s)
		}
		if subs[s] {
			return fmt.Errorf("duplicate subscription %q", s)
		}
		subs[s] = true
	}
	if subs[EventRedemption] && !perms[PermRedeem] {
		return fmt.Errorf("subscription %q requires permission %q", EventRedemption, PermRedeem)
	}
	if err := m.validateWidgets(); err != nil {
		return err
	}
	return m.validateSettings()
}

func (m *Meta) validateSettings() error {
	if m.Settings == "" {
		return nil
	}
	if err := wasmassets.ValidPath(m.Settings); err != nil {
		return fmt.Errorf("settings: %w", err)
	}
	if ext := strings.ToLower(path.Ext(m.Settings)); ext != ".html" && ext != ".htm" {
		return fmt.Errorf("settings must be an HTML file")
	}
	return nil
}

func (m *Meta) validateWidgets() error {
	if len(m.Widgets) > maxWidgets {
		return fmt.Errorf("too many widgets (max %d)", maxWidgets)
	}
	seen := map[string]bool{}
	for i := range m.Widgets {
		w := &m.Widgets[i]
		if !idRe.MatchString(w.ID) {
			return fmt.Errorf("widget id %q: a-z, 0-9 and '-', up to 64 characters", w.ID)
		}
		if seen[w.ID] {
			return fmt.Errorf("duplicate widget id %q", w.ID)
		}
		seen[w.ID] = true
		if strings.TrimSpace(w.Name) == "" || len([]rune(w.Name)) > 80 {
			return fmt.Errorf("widget %q: name must be 1..80 characters", w.ID)
		}
		if err := wasmassets.ValidPath(w.Entry); err != nil {
			return fmt.Errorf("widget %q: entry: %w", w.ID, err)
		}
		if ext := strings.ToLower(path.Ext(w.Entry)); ext != ".html" && ext != ".htm" {
			return fmt.Errorf("widget %q: entry must be an HTML file", w.ID)
		}
		if w.Width == 0 {
			w.Width = defWidgetW
		}
		if w.Height == 0 {
			w.Height = defWidgetH
		}
		if w.Width < 1 || w.Width > maxWidgetDim || w.Height < 1 || w.Height > maxWidgetDim {
			return fmt.Errorf("widget %q: size must be 1..%d", w.ID, maxWidgetDim)
		}
	}
	return nil
}

func validRoutePath(p string) error {
	if p == "/" {
		return nil
	}
	if !strings.HasPrefix(p, "/") || strings.HasSuffix(p, "/") || len(p) > 128 {
		return fmt.Errorf("path must start with '/', not end with it, up to 128 characters")
	}
	for _, seg := range strings.Split(p[1:], "/") {
		if seg == "." || seg == ".." || !pathSegRe.MatchString(seg) {
			return fmt.Errorf("bad path segment %q", seg)
		}
	}
	return nil
}

// Subscribes — подписан ли плагин на событие.
func (m *Meta) Subscribes(event string) bool {
	for _, s := range m.Subscriptions {
		if s == event {
			return true
		}
	}
	return false
}

// Requests — запросил ли плагин разрешение.
func (m *Meta) Requests(perm string) bool {
	for _, p := range m.Permissions {
		if p == perm {
			return true
		}
	}
	return false
}

// KnownPermission сообщает, существует ли такое разрешение.
func KnownPermission(p string) bool { return knownPerms[p] }
