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
	"time"

	"github.com/compico/overlaycomposer/internal/events"
	"github.com/compico/overlaycomposer/internal/wasmassets"
)

// APIVersion — версия ABI между хостом и плагином.
const APIVersion = 1

// Разрешения плагина. Плагин их запрашивает в oc_meta, пользователь выдаёт; без выдачи операция отклоняется.
const (
	PermStorage  = "storage"
	PermNetHTTP  = "net.http"
	PermNetLocal = "net.local" // HTTP к программе на этом компьютере по адресу из Meta.NetLocal (например, tosu)
	PermRedeem   = "twitch.redemption"
	PermTimer    = "timer"
	PermPublish  = "events.publish" // публиковать события для подписчиков (виджеты, аддоны, deck)
	PermChat     = "chat.send"      // писать в чат канала от имени бота
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

var knownPerms = map[string]bool{PermStorage: true, PermNetHTTP: true, PermNetLocal: true, PermRedeem: true, PermTimer: true, PermPublish: true, PermChat: true}

var knownEvents = map[string]bool{EventRedemption: true, EventTimer: true, EventHTTPResponse: true}

// Route — HTTP-маршрут плагина: он доступен как /plugins/<id>/api<Path>.
type Route struct {
	Method  string `json:"method"`
	Path    string `json:"path"`
	Handler string `json:"handler"`
}

// Meta — то, что плагин сообщает о себе в oc_meta.
type Meta struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Version     string   `json:"version"`
	Description string   `json:"description,omitempty"`
	Author      string   `json:"author,omitempty"`
	APIVersion  int      `json:"apiVersion"`
	Permissions []string `json:"permissions,omitempty"`
	// NetLocal — программы на этом компьютере, к которым плагин ходит через Fetch при выданном net.local.
	NetLocal      []NetLocalTarget `json:"netLocal,omitempty"`
	Routes        []Route          `json:"routes,omitempty"`
	Subscriptions []string         `json:"subscriptions,omitempty"`
	// ConfigSchema — необязательное описание настроек (JSON Schema-подобное), UI строит по нему форму. Хост его не толкует.
	ConfigSchema map[string]any `json:"configSchema,omitempty"`
	// Widgets — виджеты плагина (iframe с HTML из секции oc.assets); пользователь добавляет их на сцену как обычные виджеты.
	Widgets []Widget `json:"widgets,omitempty"`
	// Settings — страница настроек плагина: путь HTML-файла в статике (/plugins/<id>/ui/<settings>). Редактор открывает её
	// в sandbox-iframe в окне «Plugins → Settings»; страница говорит только с маршрутами своего плагина (oc.plugin.*).
	Settings string `json:"settings,omitempty"`
	// Events — топики, которые плагин публикует (events.publish): id, название и описание. Хост пересылает только объявленные.
	Events []EventDecl `json:"events,omitempty"`
}

// NetLocalTarget — адрес программы на этом компьютере (host:port; host — 127.0.0.1, localhost или [::1]).
// Setting — необязательное имя строковой настройки плагина, которой пользователь заменяет адрес.
type NetLocalTarget struct {
	Target  string `json:"target"`
	Setting string `json:"setting,omitempty"`
	// Paths — допустимые префиксы пути (например, "/files/beatmap/"); пусто — любой путь этой программы.
	Paths []string `json:"paths,omitempty"`
	// Streams — websocket-потоки программы, которые хост ретранслирует в события плагина (без вызовов wasm).
	Streams []NetLocalStream `json:"streams,omitempty"`
}

// NetLocalStream — websocket-поток локальной программы. Хост сам подключается к ws://<target><path> (без Origin), пока у топика
// есть подписчики, достаёт из каждого JSON-кадра поля Fields (имя → путь «a.b.c») и публикует объект из них в топик плагина.
// Кадры чаще MinIntervalMs отбрасываются без разбора, неизменившиеся значения не публикуются.
type NetLocalStream struct {
	Path          string            `json:"path"`
	Topic         string            `json:"topic"` // должен быть объявлен в events
	Fields        map[string]string `json:"fields"`
	MinIntervalMs int               `json:"minIntervalMs,omitempty"` // по умолчанию 250
}

// EventDecl — объявление публикуемого топика события.
type EventDecl struct {
	ID          string `json:"id"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
}

// EventTopics — id объявленных топиков.
func (m *Meta) EventTopics() []string {
	out := make([]string, 0, len(m.Events))
	for _, e := range m.Events {
		out = append(out, e.ID)
	}
	return out
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
	if err := m.validateNetLocal(perms[PermNetLocal]); err != nil {
		return err
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
	if err := m.validateSettings(); err != nil {
		return err
	}
	return m.validateEvents()
}

func (m *Meta) validateEvents() error {
	if err := events.ValidateDeclared(m.EventTopics()); err != nil {
		return fmt.Errorf("events: %w", err)
	}
	for _, e := range m.Events {
		if len([]rune(e.Name)) > 80 || len([]rune(e.Description)) > 300 {
			return fmt.Errorf("event %q: name up to 80 and description up to 300 characters", e.ID)
		}
	}
	return nil
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

const (
	maxNetLocal      = 4
	maxStreams       = 2  // потоков на плагин
	maxStreamFields  = 16 // полей в потоке
	defaultStreamGap = 250
	minStreamGap     = 100
	maxStreamGap     = 60000
)

var (
	fieldNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,31}$`)
	fieldPathRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*){0,7}$`)
)

var settingKeyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)

func (m *Meta) validateNetLocal(granted bool) error {
	if len(m.NetLocal) > 0 && !granted {
		return fmt.Errorf("netLocal requires permission %q", PermNetLocal)
	}
	if granted && len(m.NetLocal) == 0 {
		return fmt.Errorf("permission %q requires netLocal targets", PermNetLocal)
	}
	if len(m.NetLocal) > maxNetLocal {
		return fmt.Errorf("too many netLocal targets (max %d)", maxNetLocal)
	}
	topics := map[string]bool{}
	for _, e := range m.Events {
		topics[e.ID] = true
	}
	streams := 0
	for _, t := range m.NetLocal {
		if _, ok := normLocalTarget(t.Target); !ok {
			return fmt.Errorf("netLocal target %q: host:port on this computer expected (127.0.0.1, localhost or [::1])", t.Target)
		}
		if t.Setting != "" && !settingKeyRe.MatchString(t.Setting) {
			return fmt.Errorf("netLocal setting %q: bad name", t.Setting)
		}
		if len(t.Paths) > maxNetLocal*4 {
			return fmt.Errorf("netLocal %q: too many paths", t.Target)
		}
		for _, pr := range t.Paths {
			if !strings.HasPrefix(pr, "/") || len(pr) > 200 || hasDotSegment(pr) {
				return fmt.Errorf("netLocal path %q: must start with / and have no . or .. segments", pr)
			}
		}
		for _, s := range t.Streams {
			if streams++; streams > maxStreams {
				return fmt.Errorf("too many netLocal streams (max %d)", maxStreams)
			}
			if err := s.validate(topics); err != nil {
				return fmt.Errorf("netLocal stream %q: %w", s.Path, err)
			}
		}
	}
	return nil
}

// hasDotSegment — в пути есть сегмент «.» или «..» (с разделителями / и \): такой путь выводит за пределы разрешённого префикса.
func hasDotSegment(p string) bool {
	for _, seg := range strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == '\\' }) {
		if seg == "." || seg == ".." {
			return true
		}
	}
	return false
}

func (s NetLocalStream) validate(topics map[string]bool) error {
	if !strings.HasPrefix(s.Path, "/") || len(s.Path) > 200 || hasDotSegment(s.Path) || strings.ContainsAny(s.Path, " ?#\x00") {
		return fmt.Errorf("path must start with /, have no spaces, ?, # or . and .. segments")
	}
	if !topics[s.Topic] {
		return fmt.Errorf("topic %q is not declared in events", s.Topic)
	}
	if len(s.Fields) == 0 || len(s.Fields) > maxStreamFields {
		return fmt.Errorf("1..%d fields expected", maxStreamFields)
	}
	for name, path := range s.Fields {
		if !fieldNameRe.MatchString(name) || !fieldPathRe.MatchString(path) {
			return fmt.Errorf("field %q: bad name or path %q", name, path)
		}
	}
	if s.MinIntervalMs != 0 && (s.MinIntervalMs < minStreamGap || s.MinIntervalMs > maxStreamGap) {
		return fmt.Errorf("minIntervalMs must be %d..%d", minStreamGap, maxStreamGap)
	}
	return nil
}

func (s NetLocalStream) gap() time.Duration {
	if s.MinIntervalMs == 0 {
		return defaultStreamGap * time.Millisecond
	}
	return time.Duration(s.MinIntervalMs) * time.Millisecond
}
