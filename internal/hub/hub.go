// Package hub — состояние «что сейчас показываем» и рассылка по WebSocket.
//
// Здесь нет ни Fiber, ни GORM, ни obs-клиента: всё приходит через интерфейсы (Conn, Storage, Obs),
// поэтому логика проверяется обычными тестами без внешних зависимостей.
package hub

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"strings"
	"sync"

	"github.com/compico/overlaycomposer/internal/model"
)

// Conn — одно WebSocket-соединение. Send не должен блокироваться: медленному клиенту сообщение можно потерять.
type Conn interface {
	Send(data []byte) bool
}

// Storage — то, что хабу нужно от базы.
type Storage interface {
	GetLayout(id string) (*model.Layout, error)
	SaveLayout(l model.Layout) (stolen bool, err error)
	ResolveLayoutForScene(scene string) (*model.Layout, error)
	FirstLayoutID() (string, error)
	DefaultLayoutID() (string, error)
	RenameScene(from, to string) (bool, error)
}

// Obs — то, что хабу нужно от клиента obs-websocket.
type Obs interface {
	Connected() bool
	Scenes() []string
	LastError() string
	RefreshScenes()
}

// Peer — клиент /ws. InObs — это overlay, запущенный внутри OBS Browser Source (есть window.obsstudio).
type Peer struct {
	conn     Conn
	inObs    bool
	level    *int
	reported bool // присылал ли реальную сцену (т.е. прав достаточно)
}

type Hub struct {
	handlers []handler // расширения: обработчики сообщений по префиксу типа (chat:*)
	onGone   []func(*Peer)

	st         Storage
	obs        Obs
	overlayURL func() string
	obsEnabled func() bool

	mu             sync.Mutex
	peers          map[*Peer]struct{}
	activeLayoutID string
	currentScene   string
	sceneLayoutID  string // layout, «положенный» текущей сцене (свой или default)
	overlayScenes  []string
	sceneList      []string
}

type handler struct {
	prefix string
	fn     func(p *Peer, raw []byte)
}

// Handle подключает обработчик сообщений клиентов, тип которых начинается с prefix. Вызывать до начала работы.
func (h *Hub) Handle(prefix string, fn func(p *Peer, raw []byte)) {
	h.handlers = append(h.handlers, handler{prefix, fn})
}

// OnPeerGone вызывает fn при закрытии соединения клиента (уже без блокировки хаба). Вызывать до начала работы.
func (h *Hub) OnPeerGone(fn func(*Peer)) { h.onGone = append(h.onGone, fn) }

// Send отправляет сообщение одному клиенту; false — клиент не успевает (сообщение потеряно).
func (p *Peer) Send(msg any) bool {
	b, err := json.Marshal(msg)
	if err != nil {
		slog.Error("hub: marshal", "err", err)
		return false
	}
	return p.conn.Send(b)
}

func New(st Storage, o Obs, overlayURL func() string, obsEnabled func() bool) *Hub {
	return &Hub{
		st: st, obs: o, overlayURL: overlayURL, obsEnabled: obsEnabled,
		peers: map[*Peer]struct{}{}, overlayScenes: []string{}, sceneList: []string{},
	}
}

// ---- сообщения ----

type obsStatusMsg struct {
	Type string        `json:"type"`
	Obs  model.ObsInfo `json:"obs"`
}

type helloMsg struct {
	Type     string        `json:"type"`
	Obs      model.ObsInfo `json:"obs"`
	LayoutID *string       `json:"layoutId"`
}

type sceneChangedMsg struct {
	Type     string  `json:"type"`
	Scene    string  `json:"scene"`
	LayoutID *string `json:"layoutId"`
}

type layoutMsg struct {
	Type     string `json:"type"`
	LayoutID string `json:"layoutId,omitempty"`
}

func strp(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (h *Hub) broadcastLocked(msg any) {
	b, err := json.Marshal(msg)
	if err != nil {
		slog.Error("hub: marshal", "err", err)
		return
	}
	for p := range h.peers {
		p.conn.Send(b)
	}
}

// Broadcast — для вызовов из API (layouts:changed, sources:changed, …).
func (h *Hub) Broadcast(msg any) {
	h.mu.Lock()
	h.broadcastLocked(msg)
	h.mu.Unlock()
}

func (h *Hub) SourcesChanged() { h.Broadcast(layoutMsg{Type: "sources:changed"}) }
func (h *Hub) LayoutsChanged() { h.Broadcast(layoutMsg{Type: "layouts:changed"}) }
func (h *Hub) LayoutUpdated(id string) {
	h.Broadcast(layoutMsg{Type: "layout:updated", LayoutID: id})
}

// ---- состояние OBS ----

func (h *Hub) recomputeSceneListLocked() bool {
	next := h.overlayScenes
	if h.obs.Connected() {
		if s := h.obs.Scenes(); len(s) > 0 {
			next = s
		}
	}
	if next == nil {
		next = []string{}
	}
	if slices.Equal(next, h.sceneList) {
		return false
	}
	h.sceneList = next
	return true
}

func (h *Hub) obsInfoLocked() model.ObsInfo {
	viaOverlay, viaWs := false, h.obs.Connected()
	hosts := 0
	maxLevel := -1
	for p := range h.peers {
		if !p.inObs {
			continue
		}
		hosts++
		if p.reported {
			viaOverlay = true
		}
		if p.level != nil && *p.level > maxLevel {
			maxLevel = *p.level
		}
	}
	connected := viaOverlay || viaWs
	info := model.ObsInfo{Connected: connected, Scenes: slices.Clone(h.sceneList), Scene: strp(h.currentScene)}
	if info.Scenes == nil {
		info.Scenes = []string{}
	}
	switch {
	case viaWs:
		info.Via = new("websocket")
	case viaOverlay:
		info.Via = new("overlay")
	}
	if maxLevel >= 0 {
		info.PermissionLevel = new(maxLevel)
	}
	if !connected {
		var msg string
		switch {
		case hosts == 0:
			msg = fmt.Sprintf("Overlay не запущен внутри OBS. Добавьте в OBS источник Browser Source с URL %s "+
				"и в его свойствах поставьте Page permissions = «Read access to user information».", h.overlayURL())
		case maxLevel >= 0 && maxLevel < model.ObsMinPermission:
			msg = fmt.Sprintf("Overlay запущен в OBS, но у Browser Source мало прав (уровень %d, нужен не ниже %d). "+
				"В свойствах источника: Page permissions → «Read access to user information» или выше.", maxLevel, model.ObsMinPermission)
		default:
			msg = "Overlay запущен в OBS, ждём данные о сцене…"
		}
		if e := h.obs.LastError(); h.obsEnabled() && e != "" {
			msg += " · obs-websocket: " + e
		}
		info.Error = &msg
	}
	return info
}

func (h *Hub) ObsInfo() model.ObsInfo {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.obsInfoLocked()
}

func (h *Hub) broadcastObsStatusLocked() {
	h.broadcastLocked(obsStatusMsg{Type: "obs:status", Obs: h.obsInfoLocked()})
}

func (h *Hub) BroadcastObsStatus() {
	h.mu.Lock()
	h.broadcastObsStatusLocked()
	h.mu.Unlock()
}

// SceneList — актуальный список сцен (по запросу UI предварительно обновляем его у OBS).
func (h *Hub) SceneList(refresh bool) []string {
	if refresh && h.obs.Connected() {
		h.obs.RefreshScenes() // сетевой запрос — без блокировки хаба
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.recomputeSceneListLocked()
	return slices.Clone(h.sceneList)
}

// ApplyObsData применяет данные об OBS (от overlay или obs-websocket): список сцен и/или текущую сцену.
func (h *Hub) ApplyObsData(scene string, scenes []string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	changed := false
	if scenes != nil {
		h.overlayScenes = scenes
		if h.recomputeSceneListLocked() {
			changed = true
		}
	}
	if scene != "" && scene != h.currentScene {
		h.currentScene = scene
		// Свой layout сцены → иначе layout по умолчанию → иначе остаётся предыдущий.
		l, err := h.st.ResolveLayoutForScene(scene)
		if err != nil {
			slog.Error("hub: resolve layout", "scene", scene, "err", err)
		}
		h.sceneLayoutID = ""
		var lid *string
		if l != nil {
			h.sceneLayoutID = l.ID
			h.activeLayoutID = l.ID
			lid = &l.ID
			slog.Info("obs: сцена", "scene", scene, "layout", l.Name)
		} else {
			slog.Info("obs: сцена без layout", "scene", scene)
		}
		h.broadcastLocked(sceneChangedMsg{Type: "scene:changed", Scene: scene, LayoutID: lid})
		changed = true
	}
	if changed {
		h.broadcastObsStatusLocked()
	}
}

// Reresolve: привязки или layout по умолчанию изменились. Если текущей сцене теперь «положен» другой layout —
// переключаем; если нет — не трогаем (иначе сохранение правок в layout, открытом кнопкой ▶, сбрасывало бы показ).
func (h *Hub) Reresolve() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.currentScene == "" {
		return
	}
	l, err := h.st.ResolveLayoutForScene(h.currentScene)
	if err != nil {
		slog.Error("hub: resolve layout", "err", err)
		return
	}
	if l == nil { // сцене не «положен» никакой layout: остаётся то, что показывается сейчас
		h.sceneLayoutID = ""
		return
	}
	if l.ID == h.sceneLayoutID {
		return
	}
	h.sceneLayoutID = l.ID
	h.activeLayoutID = l.ID
	slog.Info("obs: сцена переключена на другой layout", "scene", h.currentScene, "layout", l.Name)
	h.broadcastLocked(sceneChangedMsg{Type: "scene:changed", Scene: h.currentScene, LayoutID: &l.ID})
}

// LayoutDeleted — чистит ссылки на удалённый layout.
func (h *Hub) LayoutDeleted(id string) {
	h.mu.Lock()
	if h.activeLayoutID == id {
		h.activeLayoutID = ""
	}
	if h.sceneLayoutID == id {
		h.sceneLayoutID = ""
	}
	h.mu.Unlock()
}

// ---- события obs-websocket (подключаются в main) ----

func (h *Hub) OnObsStatus(_ bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.recomputeSceneListLocked() {
		slog.Info("obs: сцен", "n", len(h.sceneList))
	}
	h.broadcastObsStatusLocked()
}

func (h *Hub) OnObsSceneList(_ []string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.recomputeSceneListLocked()
	h.broadcastObsStatusLocked()
}

func (h *Hub) OnObsScene(scene string) { h.ApplyObsData(scene, nil) }

// OnObsSceneRenamed: привязки следуют за переименованной сценой.
func (h *Hub) OnObsSceneRenamed(from, to string) {
	changed, err := h.st.RenameScene(from, to)
	if err != nil {
		slog.Error("hub: rename scene", "from", from, "to", to, "err", err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.currentScene == from {
		h.currentScene = to
	}
	if changed {
		h.broadcastLocked(layoutMsg{Type: "layouts:changed"})
	}
	h.broadcastObsStatusLocked()
}

// ---- клиенты ----

// InitialActive возвращает активный layout; если его нет — первый по имени.
func (h *Hub) InitialActive() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.initialActiveLocked()
}

func (h *Hub) initialActiveLocked() string {
	if h.activeLayoutID != "" {
		if l, _ := h.st.GetLayout(h.activeLayoutID); l != nil {
			return h.activeLayoutID
		}
	}
	id, _ := h.st.FirstLayoutID()
	h.activeLayoutID = id
	return id
}

// State — для GET /api/state.
type State struct {
	Obs             model.ObsInfo `json:"obs"`
	Scene           *string       `json:"scene"`
	ActiveLayoutID  *string       `json:"activeLayoutId"`
	DefaultLayoutID *string       `json:"defaultLayoutId"`
}

func (h *Hub) State() State {
	h.mu.Lock()
	defer h.mu.Unlock()
	def, _ := h.st.DefaultLayoutID()
	return State{Obs: h.obsInfoLocked(), Scene: strp(h.currentScene), ActiveLayoutID: strp(h.initialActiveLocked()), DefaultLayoutID: strp(def)}
}

// AddPeer регистрирует соединение и сразу шлёт ему hello.
func (h *Hub) AddPeer(c Conn) *Peer {
	p := &Peer{conn: c}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.peers[p] = struct{}{}
	b, _ := json.Marshal(helloMsg{Type: "hello", Obs: h.obsInfoLocked(), LayoutID: strp(h.initialActiveLocked())})
	c.Send(b)
	return p
}

func (h *Hub) RemovePeer(p *Peer) {
	h.mu.Lock()
	if _, ok := h.peers[p]; !ok {
		h.mu.Unlock()
		return
	}
	delete(h.peers, p)
	if p.inObs {
		h.broadcastObsStatusLocked()
	}
	h.mu.Unlock()
	for _, fn := range h.onGone {
		fn(p)
	}
}

type clientMsg struct {
	Type            string   `json:"type"`
	LayoutID        string   `json:"layoutId"`
	ObjectID        string   `json:"objectId"`
	InObs           bool     `json:"inObs"`
	PermissionLevel *float64 `json:"permissionLevel"`
	Scene           string   `json:"scene"`
	Scenes          []any    `json:"scenes"`
	Data            struct {
		Visible *bool    `json:"visible"`
		Opacity *float64 `json:"opacity"`
	} `json:"data"`
}

// HandleMessage разбирает сообщение клиента; мусор молча игнорируется.
func (h *Hub) HandleMessage(p *Peer, raw []byte) {
	var m clientMsg
	if err := json.Unmarshal(raw, &m); err != nil {
		return
	}
	switch m.Type {
	case "layout:activate":
		l, _ := h.st.GetLayout(m.LayoutID)
		if l == nil {
			return
		}
		h.mu.Lock()
		h.activeLayoutID = l.ID
		h.broadcastLocked(sceneChangedMsg{Type: "scene:changed", Scene: h.currentScene, LayoutID: &l.ID})
		h.mu.Unlock()

	case "overlay:hello":
		h.mu.Lock()
		p.inObs = m.InObs
		p.level = nil
		if m.PermissionLevel != nil && !math.IsNaN(*m.PermissionLevel) {
			p.level = new(int(*m.PermissionLevel))
		}
		p.reported = false
		h.broadcastObsStatusLocked()
		h.mu.Unlock()

	case "obs:report":
		h.mu.Lock()
		inObs := p.inObs
		h.mu.Unlock()
		if !inObs { // сцену принимаем только от overlay, объявившего себя «внутри OBS»
			return
		}
		var scenes []string
		if m.Scenes != nil {
			scenes = make([]string, 0, len(m.Scenes))
			for _, x := range m.Scenes {
				if s, ok := x.(string); ok && len(scenes) < 500 {
					scenes = append(scenes, s)
				}
			}
		}
		if m.Scene != "" {
			h.mu.Lock()
			p.reported = true
			h.mu.Unlock()
		}
		h.ApplyObsData(m.Scene, scenes)
		if m.Scene != "" {
			h.BroadcastObsStatus() // p.reported мог измениться, даже если сцена прежняя
		}

	case "source:update":
		h.mu.Lock()
		id := m.LayoutID
		if id == "" {
			id = h.activeLayoutID
		}
		h.mu.Unlock()
		if id == "" {
			return
		}
		l, _ := h.st.GetLayout(id)
		if l == nil {
			return
		}
		i := slices.IndexFunc(l.Objects, func(o model.Object) bool { return o.ID == m.ObjectID })
		if i < 0 {
			return
		}
		if m.Data.Visible != nil {
			l.Objects[i].Visible = *m.Data.Visible
		}
		if m.Data.Opacity != nil {
			l.Objects[i].Opacity = math.Min(1, math.Max(0, *m.Data.Opacity))
		}
		if _, err := h.st.SaveLayout(*l); err != nil {
			slog.Error("hub: save layout", "err", err)
			return
		}
		h.LayoutUpdated(l.ID)

	default:
		for _, hd := range h.handlers {
			if strings.HasPrefix(m.Type, hd.prefix) {
				hd.fn(p, raw)
				return
			}
		}
	}
}
