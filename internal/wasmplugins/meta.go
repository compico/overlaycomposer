// Package wasmplugins — автономные (standalone) плагины на WebAssembly.
//
// Плагин — один .wasm-файл на любом языке (Rust, Go, …), который реализует небольшой JSON-ABI (см. docs/WASM_PLUGINS.md).
// Манифеста нет: метаданные плагин отдаёт сам через экспорт oc_meta. Плагин пассивен: он отвечает на маршруты и события,
// а долгоживущими ресурсами (таймеры, сетевые запросы, EventSub) владеет хост.
package wasmplugins

import (
	"encoding/json"
	"fmt"
	"github.com/compico/overlaycomposer/internal/plugins"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/compico/overlaycomposer/internal/events"
	"github.com/compico/overlaycomposer/internal/wasmassets"
)

// APIVersion — версия API плагинов, которую понимает этот хост. Плагин в oc_meta.apiVersion объявляет, какая версия API ему нужна:
// хост принимает плагины с apiVersion от 1 до APIVersion, более новые отклоняет с просьбой обновить приложение.
// Поднимайте её на 1 в том же PR, где в API появляется новое право, событие или поле меты, и записывайте здесь, что вошло:
//
//	1 — всё до v2026.11.23.6 включительно;
//	2 — право chat.read и событие chat.message, поле Widget.Surfaces (область виджета).
const APIVersion = 2

// minAPI — с какой версии API доступны право или событие (нет в карте — с первой). Плагин, который их использует, обязан
// объявить apiVersion не ниже: так более старый хост отклонит его по версии, а не по незнакомому названию.
var (
	permMinAPI  = map[string]int{PermChatRead: 2}
	eventMinAPI = map[string]int{EventChatMessage: 2}
)

// APITooNewError — плагину нужна более новая версия API, чем понимает этот хост.
type APITooNewError struct{ Need, Have int }

func (e *APITooNewError) Error() string {
	return fmt.Sprintf("the plugin needs plugin API version %d, this Overlay Composer supports up to %d: update the app", e.Need, e.Have)
}

// Разрешения плагина. Плагин их запрашивает в oc_meta, пользователь выдаёт; без выдачи операция отклоняется.
const (
	PermStorage         = "storage"
	PermNetHTTP         = "net.http"
	PermNetLocal        = "net.local" // HTTP к программе на этом компьютере по адресу из Meta.NetLocal (например, tosu)
	PermRedeem          = "twitch.redemption"
	PermTimer           = "timer"
	PermPublish         = "events.publish"    // публиковать события для подписчиков (виджеты, аддоны, deck)
	PermChat            = "chat.send"         // писать в чат канала от имени бота
	PermChatCommandRead = "chat.command.read" // получать команды чата («!np …») из Meta.Commands; весь чат плагин не видит
	PermChatRead        = "chat.read"         // получать все сообщения чата канала по умолчанию (событие chat.message)
	maxRoutes           = 64
	maxMetaBytes        = 64 << 10
	maxWidgets          = 16
	maxWidgetSettings   = 32
	defWidgetW          = 400
	defWidgetH          = 300
	maxWidgetDim        = 8192
)

// События, на которые плагин может подписаться.
const (
	EventRedemption   = "twitch.redemption"
	EventChatCommand  = "chat.command"
	EventChatMessage  = "chat.message" // message | delete | clear | status; нужно разрешение chat.read
	EventTimer        = "timer"
	EventHTTPResponse = "http.response"
)

var (
	idRe      = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
	semverRe  = regexp.MustCompile(`^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`)
	handlerRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)
	pathSegRe = regexp.MustCompile(`^[A-Za-z0-9._~-]+$`)
)

var knownPerms = map[string]bool{PermStorage: true, PermNetHTTP: true, PermNetLocal: true, PermRedeem: true, PermTimer: true, PermPublish: true, PermChat: true, PermChatCommandRead: true, PermChatRead: true}

var knownEvents = map[string]bool{EventRedemption: true, EventChatCommand: true, EventChatMessage: true, EventTimer: true, EventHTTPResponse: true}

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
	// Commands — команды чата без «!» (в нижнем регистре), которые плагин хочет получать событием chat.command. Остальные команды ему
	// не доставляются. Нужны подписка chat.command и разрешение chat.command.read.
	Commands []string `json:"commands,omitempty"`
	// ConfigSchema — необязательное описание настроек (JSON Schema-подобное), UI строит по нему форму. Хост его не толкует.
	ConfigSchema map[string]any `json:"configSchema,omitempty"`
	// Widgets — виджеты плагина (iframe с HTML из секции oc.assets); пользователь добавляет их на сцену как обычные виджеты.
	Widgets []Widget `json:"widgets,omitempty"`
	// Settings — страница настроек плагина: путь HTML-файла в статике (/plugins/<id>/ui/<settings>). Редактор открывает её
	// в sandbox-iframe в окне «Plugins → Settings»; страница говорит только с маршрутами своего плагина (oc.plugin.*).
	Settings string `json:"settings,omitempty"`
	// Actions — действия, которые плагин предлагает панели Deck и другим «пультам»: по нажатию OC вызывает маршрут плагина.
	Actions []Action `json:"actions,omitempty"`
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

// Области, в которых виджет можно показывать. Overlay — сцена OBS (источник на сцене), Dashboard — окно управления стримом (/),
// Deck — панель на планшете. У виджета один тип на все области: он сам решает, как выглядеть, по info.surface из oc.ready.
const (
	SurfaceOverlay   = "overlay"
	SurfaceDashboard = "dashboard"
	SurfaceDeck      = "deck"
)

// IsSurface — известная область.
func IsSurface(s string) bool {
	return s == SurfaceOverlay || s == SurfaceDashboard || s == SurfaceDeck
}

// Widget — виджет плагина. Entry — путь HTML-файла в статике плагина (/plugins/<id>/ui/<entry>).
type Widget struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Entry  string `json:"entry"`
	Width  int    `json:"width,omitempty"`
	Height int    `json:"height,omitempty"`
	// Settings — настройки источника на сцене (формат как в plugin.json HTML-плагинов): редактор строит по ним форму,
	// значения уходят виджету как ?key=value (читайте через oc.param). У каждого источника на сцене они свои.
	Settings []plugins.Setting `json:"settings,omitempty"`
	// Surfaces — где виджет можно показывать (overlay, dashboard, deck). Пусто — только overlay, как было до появления областей.
	// После Validate всегда заполнено.
	Surfaces []string `json:"surfaces,omitempty"`
}

// HasSurface — виджет можно показывать в этой области.
func (w Widget) HasSurface(s string) bool {
	for _, x := range w.Surfaces {
		if x == s {
			return true
		}
	}
	return len(w.Surfaces) == 0 && s == SurfaceOverlay
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
	if m.APIVersion < 1 {
		return fmt.Errorf("apiVersion %d: 1 or more expected", m.APIVersion)
	}
	if m.APIVersion > APIVersion { // раньше проверок прав и подписок: право из новой версии API хост ещё не знает
		return &APITooNewError{Need: m.APIVersion, Have: APIVersion}
	}
	perms := map[string]bool{}
	for _, p := range m.Permissions {
		if !knownPerms[p] {
			return fmt.Errorf("unknown permission %q", p)
		}
		if perms[p] {
			return fmt.Errorf("duplicate permission %q", p)
		}
		if min := permMinAPI[p]; m.APIVersion < min {
			return fmt.Errorf("permission %q needs apiVersion %d or more", p, min)
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
		if min := eventMinAPI[s]; m.APIVersion < min {
			return fmt.Errorf("subscription %q needs apiVersion %d or more", s, min)
		}
		subs[s] = true
	}
	if subs[EventRedemption] && !perms[PermRedeem] {
		return fmt.Errorf("subscription %q requires permission %q", EventRedemption, PermRedeem)
	}
	if subs[EventChatCommand] && !perms[PermChatCommandRead] {
		return fmt.Errorf("subscription %q requires permission %q", EventChatCommand, PermChatCommandRead)
	}
	if subs[EventChatMessage] && !perms[PermChatRead] {
		return fmt.Errorf("subscription %q requires permission %q", EventChatMessage, PermChatRead)
	}
	if err := m.validateCommands(subs[EventChatCommand]); err != nil {
		return err
	}
	if err := m.validateActions(); err != nil {
		return err
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
		if len(w.Surfaces) == 0 {
			w.Surfaces = []string{SurfaceOverlay}
		}
		seenSurf := map[string]bool{}
		for _, s := range w.Surfaces {
			if !IsSurface(s) {
				return fmt.Errorf("widget %q: unknown surface %q (overlay, dashboard, deck)", w.ID, s)
			}
			if seenSurf[s] {
				return fmt.Errorf("widget %q: duplicate surface %q", w.ID, s)
			}
			seenSurf[s] = true
		}
		if len(w.Settings) > maxWidgetSettings {
			return fmt.Errorf("widget %q: too many settings (max %d)", w.ID, maxWidgetSettings)
		}
		// Схему настроек проверяет тот же код, что и у HTML-плагинов (ключи, типы, options у select; пустые подписи заполняются).
		if err := (&plugins.Manifest{ID: "widget", Name: w.Name, Settings: w.Settings}).Validate(); err != nil {
			return fmt.Errorf("widget %q: %w", w.ID, err)
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

const maxCommands = 16

var commandRe = regexp.MustCompile(`^[a-z0-9_]{1,32}$`)

func (m *Meta) validateCommands(subscribed bool) error {
	if len(m.Commands) > maxCommands {
		return fmt.Errorf("too many commands (max %d)", maxCommands)
	}
	if subscribed && len(m.Commands) == 0 {
		return fmt.Errorf("subscription %q requires at least one command in commands", EventChatCommand)
	}
	if !subscribed && len(m.Commands) > 0 {
		return fmt.Errorf("commands require subscription %q", EventChatCommand)
	}
	seen := map[string]bool{}
	for _, c := range m.Commands {
		if !commandRe.MatchString(c) {
			return fmt.Errorf("command %q: lowercase latin letters, digits and _ only (1..32), without !", c)
		}
		if seen[c] {
			return fmt.Errorf("duplicate command %q", c)
		}
		seen[c] = true
	}
	return nil
}

// WantsCommand — просил ли плагин команду name (без «!», в нижнем регистре).
func (m *Meta) WantsCommand(name string) bool {
	for _, c := range m.Commands {
		if c == name {
			return true
		}
	}
	return false
}

// ChatCommand — команда чата, доставленная плагину событием chat.command.
type ChatCommand struct {
	Command       string   `json:"command"` // «np» для «!np»: без «!», в нижнем регистре
	Args          []string `json:"args"`    // слова после команды (разделитель — пробел), регистр сохранён
	Channel       string   `json:"channel"`
	UserID        string   `json:"userId"`
	User          string   `json:"user"` // ник (login)
	DisplayName   string   `json:"displayName"`
	MessageID     string   `json:"messageId"` // id сообщения в Twitch
	IsBroadcaster bool     `json:"isBroadcaster"`
	IsMod         bool     `json:"isMod"`
	IsVip         bool     `json:"isVip"`
	IsSub         bool     `json:"isSub"`
	Timestamp     int64    `json:"timestamp"` // мс
}

// Action — действие плагина для плитки панели Deck: нажатие выполняет запрос Method Path к маршруту этого плагина с готовым Body.
// Хост вызывает только объявленные действия, поэтому устройство с панелью не может попросить плагин о чём-то ещё.
type Action struct {
	ID     string          `json:"id"`               // a-z, 0-9, _ и -, до 32 символов; уникален в плагине
	Title  string          `json:"title"`            // подпись в редакторе и на плитке по умолчанию
	Icon   string          `json:"icon,omitempty"`   // имя значка Deck (play, pause, next, ban…), необязательно
	Method string          `json:"method,omitempty"` // по умолчанию POST
	Path   string          `json:"path"`             // маршрут из Meta.Routes
	Body   json.RawMessage `json:"body,omitempty"`   // готовое тело запроса (JSON, до 1 КиБ)
	// Danger — действие необратимо или заметно зрителям (бан трека, остановка): плитка по умолчанию просит подтверждения.
	Danger bool `json:"danger,omitempty"`
	// ActiveWhen — когда плитка подсвечена: последнее событие топика плагина (из Events) удовлетворяет условию. Необязательно.
	ActiveWhen *ActiveWhen `json:"activeWhen,omitempty"`
	// Display — текст на плитке из событий плагина (например, «сейчас играет» на крупной плитке). Необязательно.
	Display *Display `json:"display,omitempty"`
}

// Display — текст плитки Deck из последнего события топика: каждое поле Fields (путь «a.b») даёт строку. Строка — как есть,
// число — числом, массив строк — через запятую; пустые и отсутствующие поля пропускаются. Строки обрезаются по maxDisplayRunes.
type Display struct {
	Topic  string   `json:"topic"`
	Fields []string `json:"fields"`
}

const (
	maxDisplayFields = 4
	maxDisplayRunes  = 80
)

func (m *Meta) validateDisplay(a *Action) error {
	d := a.Display
	if d == nil {
		return nil
	}
	declared := false
	for _, e := range m.Events {
		if e.ID == d.Topic {
			declared = true
		}
	}
	if !declared {
		return fmt.Errorf("action %q: display.topic %q is not declared in events", a.ID, d.Topic)
	}
	if len(d.Fields) == 0 || len(d.Fields) > maxDisplayFields {
		return fmt.Errorf("action %q: display.fields needs 1 to %d paths", a.ID, maxDisplayFields)
	}
	for _, f := range d.Fields {
		if !activeFieldRe.MatchString(f) {
			return fmt.Errorf("action %q: display.fields must be paths like a.b (letters, digits, _; up to 4 parts)", a.ID)
		}
	}
	return nil
}

// jsonPath достаёт значение по пути «a.b» из JSON-объекта.
func jsonPath(v any, path string) (any, bool) {
	for _, k := range strings.Split(path, ".") {
		o, ok := v.(map[string]any)
		if !ok {
			return nil, false
		}
		if v, ok = o[k]; !ok {
			return nil, false
		}
	}
	return v, true
}

// Lines — строки текста плитки из значения события.
func (d Display) Lines(data json.RawMessage) []string {
	var v any
	if json.Unmarshal(data, &v) != nil {
		return nil
	}
	var out []string
	for _, f := range d.Fields {
		x, ok := jsonPath(v, f)
		if !ok {
			continue
		}
		var s string
		switch t := x.(type) {
		case string:
			s = t
		case float64:
			s = strconv.FormatFloat(t, 'f', -1, 64)
		case []any:
			parts := make([]string, 0, len(t))
			for _, e := range t {
				if es, ok := e.(string); ok && strings.TrimSpace(es) != "" {
					parts = append(parts, strings.TrimSpace(es))
				}
			}
			s = strings.Join(parts, ", ")
		}
		s = strings.Join(strings.Fields(s), " ")
		if r := []rune(s); len(r) > maxDisplayRunes {
			s = string(r[:maxDisplayRunes-1]) + "…"
		}
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// ActiveWhen — условие подсветки плитки Deck по событиям плагина. Topic — объявленный в Events топик. Field — путь «a.b» к полю
// объекта события (пусто — само значение события). Equals — скаляр для сравнения (строка, число, bool); без него условие
// выполнено, когда значение «истинно» (true, ненулевое число, непустая строка).
type ActiveWhen struct {
	Topic  string          `json:"topic"`
	Field  string          `json:"field,omitempty"`
	Equals json.RawMessage `json:"equals,omitempty"`
}

var activeFieldRe = regexp.MustCompile(`^[A-Za-z0-9_]+(\.[A-Za-z0-9_]+){0,3}$`)

const maxActiveEquals = 120

func (m *Meta) validateActiveWhen(a *Action) error {
	w := a.ActiveWhen
	if w == nil {
		return nil
	}
	declared := false
	for _, e := range m.Events {
		if e.ID == w.Topic {
			declared = true
		}
	}
	if !declared {
		return fmt.Errorf("action %q: activeWhen.topic %q is not declared in events", a.ID, w.Topic)
	}
	if w.Field != "" && !activeFieldRe.MatchString(w.Field) {
		return fmt.Errorf("action %q: activeWhen.field must be a path like a.b (letters, digits, _; up to 4 parts)", a.ID)
	}
	if len(w.Equals) > 0 {
		var v any
		if len(w.Equals) > maxActiveEquals || json.Unmarshal(w.Equals, &v) != nil {
			return fmt.Errorf("action %q: activeWhen.equals must be a short JSON scalar", a.ID)
		}
		switch v.(type) {
		case string, float64, bool:
		default:
			return fmt.Errorf("action %q: activeWhen.equals must be a string, number or boolean", a.ID)
		}
	}
	return nil
}

// Match сообщает, удовлетворяет ли значение события условию.
func (w ActiveWhen) Match(data json.RawMessage) bool {
	var v any
	if json.Unmarshal(data, &v) != nil {
		return false
	}
	if w.Field != "" {
		for _, k := range strings.Split(w.Field, ".") {
			o, ok := v.(map[string]any)
			if !ok {
				return false
			}
			if v, ok = o[k]; !ok {
				return false
			}
		}
	}
	if len(w.Equals) > 0 {
		var want any
		if json.Unmarshal(w.Equals, &want) != nil {
			return false
		}
		return v == want
	}
	switch x := v.(type) {
	case bool:
		return x
	case float64:
		return x != 0
	case string:
		return x != ""
	}
	return false
}

const (
	maxActions    = 32
	maxActionBody = 1 << 10
	maxActionText = 40
)

var actionIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

func (m *Meta) validateActions() error {
	if len(m.Actions) > maxActions {
		return fmt.Errorf("too many actions (max %d)", maxActions)
	}
	seen := map[string]bool{}
	for i := range m.Actions {
		a := &m.Actions[i]
		if !actionIDRe.MatchString(a.ID) || seen[a.ID] {
			return fmt.Errorf("action %q: id a-z, 0-9, _ and -, up to 32 characters, unique", a.ID)
		}
		seen[a.ID] = true
		if t := strings.TrimSpace(a.Title); t == "" || len([]rune(t)) > maxActionText || strings.ContainsAny(t, "\r\n\t") {
			return fmt.Errorf("action %q: title is required, up to %d characters, one line", a.ID, maxActionText)
		}
		if len(a.Icon) > 16 {
			return fmt.Errorf("action %q: icon name is too long", a.ID)
		}
		if a.Method == "" {
			a.Method = "POST"
		}
		if !m.hasRoute(a.Method, a.Path) {
			return fmt.Errorf("action %q: route %s %s is not declared in routes", a.ID, a.Method, a.Path)
		}
		if len(a.Body) > maxActionBody || (len(a.Body) > 0 && !json.Valid(a.Body)) {
			return fmt.Errorf("action %q: body must be JSON up to %d bytes", a.ID, maxActionBody)
		}
		if err := m.validateActiveWhen(a); err != nil {
			return err
		}
		if err := m.validateDisplay(a); err != nil {
			return err
		}
	}
	return nil
}

func (m *Meta) hasRoute(method, path string) bool {
	for _, r := range m.Routes {
		if r.Method == method && r.Path == path {
			return true
		}
	}
	return false
}

// FindAction — объявленное действие плагина по id.
func (m *Meta) FindAction(id string) (Action, bool) {
	for _, a := range m.Actions {
		if a.ID == id {
			return a, true
		}
	}
	return Action{}, false
}
