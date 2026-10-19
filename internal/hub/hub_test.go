package hub

import (
	"encoding/json"
	"slices"
	"sync"
	"testing"

	"github.com/compico/overlaycomposer/internal/model"
)

// ---- фейки ----

type fakeStore struct {
	layouts map[string]*model.Layout
	def     string
}

func (f *fakeStore) clone(l *model.Layout) *model.Layout {
	c := *l
	c.Scenes = slices.Clone(l.Scenes)
	c.Objects = slices.Clone(l.Objects)
	return &c
}
func (f *fakeStore) GetLayout(id string) (*model.Layout, error) {
	if l, ok := f.layouts[id]; ok {
		return f.clone(l), nil
	}
	return nil, nil
}
func (f *fakeStore) SaveLayout(l model.Layout) (bool, error) {
	stolen := false
	for id, o := range f.layouts {
		if id == l.ID {
			continue
		}
		var rest []string
		for _, s := range o.Scenes {
			if slices.Contains(l.Scenes, s) {
				stolen = true
			} else {
				rest = append(rest, s)
			}
		}
		o.Scenes = rest
	}
	f.layouts[l.ID] = f.clone(&l)
	return stolen, nil
}
func (f *fakeStore) ResolveLayoutForScene(scene string) (*model.Layout, error) {
	for _, l := range f.layouts {
		if slices.Contains(l.Scenes, scene) {
			return f.clone(l), nil
		}
	}
	return f.GetLayout(f.def)
}
func (f *fakeStore) FirstLayoutID() (string, error)   { return "gaming", nil }
func (f *fakeStore) DefaultLayoutID() (string, error) { return f.def, nil }
func (f *fakeStore) RenameScene(from, to string) (bool, error) {
	ch := false
	for _, l := range f.layouts {
		for i, s := range l.Scenes {
			if s == from {
				l.Scenes[i] = to
				ch = true
			}
		}
	}
	return ch, nil
}

type fakeObs struct {
	connected bool
	scenes    []string
}

func (o *fakeObs) Connected() bool   { return o.connected }
func (o *fakeObs) Scenes() []string  { return o.scenes }
func (o *fakeObs) LastError() string { return "" }
func (o *fakeObs) RefreshScenes()    {}

type fakeConn struct {
	mu   sync.Mutex
	msgs []map[string]any
}

func (c *fakeConn) Send(b []byte) bool {
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	c.mu.Lock()
	c.msgs = append(c.msgs, m)
	c.mu.Unlock()
	return true
}
func (c *fakeConn) reset() { c.mu.Lock(); c.msgs = nil; c.mu.Unlock() }
func (c *fakeConn) has(typ, layout string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, m := range c.msgs {
		if m["type"] == typ && (layout == "" || m["layoutId"] == layout) {
			return true
		}
	}
	return false
}

func newHub() (*Hub, *fakeStore, *fakeObs, *fakeConn) {
	st := &fakeStore{layouts: map[string]*model.Layout{
		"gaming": {ID: "gaming", Name: "Gaming", Scenes: []string{"Gaming"}},
		"legacy": {ID: "legacy", Name: "Chatting", Scenes: []string{"Chatting"}},
		"pause":  {ID: "pause", Name: "Pause"},
	}, def: "gaming"}
	o := &fakeObs{connected: true, scenes: []string{"Gaming", "BRB", "Chatting"}}
	h := New(st, o, func() string { return "http://127.0.0.1:8000/overlay" }, func() bool { return true })
	c := &fakeConn{}
	h.AddPeer(c)
	return h, st, o, c
}

func TestSceneRouting(t *testing.T) {
	h, st, _, c := newHub()
	h.OnObsStatus(true)
	if got := h.ObsInfo(); !got.Connected || *got.Via != "websocket" || !slices.Equal(got.Scenes, []string{"Gaming", "BRB", "Chatting"}) {
		t.Fatalf("obs info: %+v", got)
	}

	h.OnObsScene("Chatting")
	if !c.has("scene:changed", "legacy") {
		t.Fatal("Chatting → legacy")
	}
	c.reset()
	h.OnObsScene("BRB") // нет привязки → default
	if !c.has("scene:changed", "gaming") {
		t.Fatal("BRB → default gaming")
	}

	// привязка BRB к pause: текущая сцена переключается сразу
	c.reset()
	l, _ := st.GetLayout("pause")
	l.Scenes = []string{"BRB"}
	st.SaveLayout(*l)
	h.Reresolve()
	if !c.has("scene:changed", "pause") {
		t.Fatal("BRB → pause after binding")
	}

	// «кража»: gaming забирает BRB
	c.reset()
	g, _ := st.GetLayout("gaming")
	g.Scenes = []string{"Gaming", "BRB"}
	st.SaveLayout(*g)
	h.Reresolve()
	if p, _ := st.GetLayout("pause"); len(p.Scenes) != 0 {
		t.Fatalf("pause scenes: %v", p.Scenes)
	}
	if !c.has("scene:changed", "gaming") {
		t.Fatal("BRB back to gaming")
	}

	// повторное сохранение без смены привязки не сбрасывает ручной выбор layout
	h.HandleMessage(h.peerOf(c), []byte(`{"type":"layout:activate","layoutId":"pause"}`))
	c.reset()
	h.Reresolve()
	if c.has("scene:changed", "") {
		t.Fatal("Reresolve must not override manual activation")
	}

	// смена default
	st.def = "pause"
	h.OnObsScene("Unknown")
	if !c.has("scene:changed", "pause") {
		t.Fatal("Unknown → new default")
	}

	// переименование переносит привязку и текущую сцену
	h.OnObsSceneRenamed("BRB", "Pause scene")
	if g, _ := st.GetLayout("gaming"); !slices.Equal(g.Scenes, []string{"Gaming", "Pause scene"}) {
		t.Fatalf("rename: %v", g.Scenes)
	}
}

func (h *Hub) peerOf(c Conn) *Peer {
	h.mu.Lock()
	defer h.mu.Unlock()
	for p := range h.peers {
		if p.conn == c {
			return p
		}
	}
	return nil
}

func TestOverlayReport(t *testing.T) {
	st := &fakeStore{layouts: map[string]*model.Layout{"a": {ID: "a", Name: "A", Scenes: []string{"S1"}}}}
	o := &fakeObs{}
	h := New(st, o, func() string { return "u" }, func() bool { return false })
	c := &fakeConn{}
	p := h.AddPeer(c)

	if info := h.ObsInfo(); info.Connected || info.Error == nil {
		t.Fatalf("expected disconnected with error: %+v", info)
	}
	// отчёт до hello игнорируется
	h.HandleMessage(p, []byte(`{"type":"obs:report","scene":"S1"}`))
	if c.has("scene:changed", "") {
		t.Fatal("report before hello must be ignored")
	}
	h.HandleMessage(p, []byte(`{"type":"overlay:hello","inObs":true,"permissionLevel":1}`))
	if info := h.ObsInfo(); info.Error == nil || info.PermissionLevel == nil || *info.PermissionLevel != 1 {
		t.Fatalf("low permission: %+v", info)
	}
	h.HandleMessage(p, []byte(`{"type":"obs:report","scene":"S1","scenes":["S1","S2",5]}`))
	info := h.ObsInfo()
	if !info.Connected || *info.Via != "overlay" || !slices.Equal(info.Scenes, []string{"S1", "S2"}) {
		t.Fatalf("after report: %+v", info)
	}
	if !c.has("scene:changed", "a") {
		t.Fatal("scene:changed a")
	}
	h.HandleMessage(p, []byte(`{"type":"nonsense"`)) // битый JSON не паникует
	h.RemovePeer(p)
	if info := h.ObsInfo(); info.Connected {
		t.Fatal("peer gone → disconnected")
	}
}

func TestSourceUpdate(t *testing.T) {
	st := &fakeStore{layouts: map[string]*model.Layout{"a": {ID: "a", Name: "A", Objects: []model.Object{{ID: "o1", Visible: true, Opacity: 1}}}}}
	h := New(st, &fakeObs{}, func() string { return "u" }, func() bool { return false })
	c := &fakeConn{}
	p := h.AddPeer(c)
	h.HandleMessage(p, []byte(`{"type":"source:update","layoutId":"a","objectId":"o1","data":{"visible":false,"opacity":7}}`))
	l, _ := st.GetLayout("a")
	if l.Objects[0].Visible || l.Objects[0].Opacity != 1 {
		t.Fatalf("object: %+v", l.Objects[0])
	}
	if !c.has("layout:updated", "a") {
		t.Fatal("layout:updated")
	}
}
