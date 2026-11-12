package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"

	"github.com/compico/overlaycomposer/internal/obs"
	"github.com/compico/overlaycomposer/internal/store"
)

type fakeDeckObs struct {
	connected bool
	scene     string
	scenes    []string
	set       []string
	err       error
}

func (f *fakeDeckObs) Connected() bool      { return f.connected }
func (f *fakeDeckObs) CurrentScene() string { return f.scene }
func (f *fakeDeckObs) Scenes() []string     { return f.scenes }
func (f *fakeDeckObs) SetScene(n string) error {
	if f.err != nil {
		return f.err
	}
	f.set = append(f.set, n)
	f.scene = n
	return nil
}

func deckEnv(t *testing.T) (*Server, *fakeDeckObs) {
	st, err := store.Open(filepath.Join(t.TempDir(), "oc.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	o := &fakeDeckObs{connected: true, scene: "Game", scenes: []string{"Game", "Chat"}}
	s := &Server{d: Deps{Store: st}, deckObsOverride: o}
	s.app = fiber.New()
	s.deckRoutes()
	return s, o
}

func deckReq(t *testing.T, s *Server, method, path, body string, hdr bool) (int, string, http.Header) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if hdr {
		req.Header.Set("X-Overlay-Composer", "1")
	}
	resp, err := s.app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header
}

const deckLayoutJSON = `{"rev":%d,"groups":[{"id":"main","name":"Main","tiles":[
 {"id":"game","x":0,"y":0,"w":2,"h":1,"label":"Game","color":"blue","action":{"type":"scene","scene":"Game"}},
 {"id":"chat","x":0,"y":1,"w":2,"h":1,"label":"Chat","confirm":true,"action":{"type":"scene","scene":"Chat"}},
 {"id":"gone","x":2,"y":0,"w":1,"h":1,"action":{"type":"scene","scene":"Deleted"}}]},{"id":"two","name":"Two","tiles":[]}]}`

func saveDeck(t *testing.T, s *Server, rev int) {
	t.Helper()
	raw := strings.Replace(deckLayoutJSON, "%d", string(rune('0'+rev)), 1)
	if code, b, _ := deckReq(t, s, "PUT", "/api/deck/layout", raw, true); code != 200 {
		t.Fatalf("save: %d %s", code, b)
	}
}

func TestDeckPageIsRenderedOnTheServerWithDefaultLayout(t *testing.T) {
	s, _ := deckEnv(t)
	code, html, h := deckReq(t, s, "GET", "/deck", "", false)
	if code != 200 || !strings.Contains(html, `data-scene="Game"`) || !strings.Contains(html, `data-scene="Chat"`) {
		t.Fatalf("%d %s", code, html)
	}
	if !strings.Contains(html, "c-blue on") {
		t.Fatalf("активная сцена должна подсвечиваться")
	}
	if !strings.Contains(h.Get("Content-Security-Policy"), "default-src 'none'") || h.Get("Cache-Control") != "no-store" {
		t.Fatalf("заголовки: %v", h)
	}
	if code, b, _ := deckReq(t, s, "GET", "/deck/probe", "", false); code != 200 || !strings.Contains(b, "WebSocket") {
		t.Fatalf("probe: %d", code)
	}
}

func TestDeckPressRunsOnlyWhatTheSavedLayoutSays(t *testing.T) {
	s, o := deckEnv(t)
	saveDeck(t, s, 0)
	press := func(body string, hdr bool) (int, string) {
		c, b, _ := deckReq(t, s, "POST", "/api/deck/press", body, hdr)
		return c, b
	}
	if c, _ := press(`{"tile":"game"}`, false); c != 400 {
		t.Fatalf("без заголовка приложения (защита от чужих страниц): %d", c)
	}
	if c, b := press(`{"tile":"game"}`, true); c != 200 || len(o.set) != 1 || o.set[0] != "Game" {
		t.Fatalf("press: %d %s %v", c, b, o.set)
	}
	// клиент не может подсунуть сцену: присланные поля игнорируются, неизвестная плитка — 404
	if c, _ := press(`{"tile":"x","scene":"Evil","action":{"type":"scene","scene":"Evil"}}`, true); c != 404 || len(o.set) != 1 {
		t.Fatalf("неизвестная плитка: %d %v", c, o.set)
	}
	if c, _ := press(`{"tile":"game","scene":"Chat"}`, true); c != 200 || o.set[len(o.set)-1] != "Game" {
		t.Fatalf("сцену задаёт раскладка: %v", o.set)
	}
	// опасная плитка: без confirm — 409 и ничего не происходит, со вторым касанием — выполняется
	n := len(o.set)
	if c, b := press(`{"tile":"chat"}`, true); c != 409 || !strings.Contains(b, `"confirm":true`) || len(o.set) != n {
		t.Fatalf("confirm: %d %s", c, b)
	}
	if c, _ := press(`{"tile":"chat","confirm":true}`, true); c != 200 || o.set[len(o.set)-1] != "Chat" {
		t.Fatalf("confirmed: %v", o.set)
	}
	// сцена, которой уже нет в OBS, — 404; OBS не подключён — 503; ошибка OBS — 502
	o.err = obs.ErrUnknownScene
	if c, _ := press(`{"tile":"gone"}`, true); c != 404 {
		t.Fatalf("unknown scene: %d", c)
	}
	o.err = errors.New("boom")
	if c, _ := press(`{"tile":"game"}`, true); c != 502 {
		t.Fatalf("obs error: %d", c)
	}
	o.err, o.connected = nil, false
	if c, _ := press(`{"tile":"game"}`, true); c != 503 {
		t.Fatalf("offline: %d", c)
	}
	if c, _ := press(`not json`, true); c != 400 {
		t.Fatalf("bad body: %d", c)
	}
}

func TestDeckPressIsRateLimited(t *testing.T) {
	s, _ := deckEnv(t)
	saveDeck(t, s, 0)
	limited := 0
	for i := 0; i < deckBurst+10; i++ {
		if c, _, _ := deckReq(t, s, "POST", "/api/deck/press", `{"tile":"game"}`, true); c == 429 {
			limited++
		}
	}
	if limited < 8 {
		t.Fatalf("лишние нажатия должны отклоняться: %d", limited)
	}
}

func TestDeckLayoutSaveConflictAndReset(t *testing.T) {
	s, _ := deckEnv(t)
	var st struct {
		Rev    int  `json:"rev"`
		Online bool `json:"online"`
	}
	state := func() {
		_, b, _ := deckReq(t, s, "GET", "/api/deck/state", "", false)
		st.Rev = -1
		_ = json.Unmarshal([]byte(b), &st)
	}
	state()
	if st.Rev != 0 || !st.Online {
		t.Fatalf("начальное состояние: %+v", st)
	}
	saveDeck(t, s, 0)
	state()
	if st.Rev != 1 {
		t.Fatalf("после сохранения rev=1: %+v", st)
	}
	// устаревшая ревизия (вторая вкладка редактора) — 409, ничего не затирается
	if c, _, _ := deckReq(t, s, "PUT", "/api/deck/layout", strings.Replace(deckLayoutJSON, "%d", "0", 1), true); c != 409 {
		t.Fatalf("конфликт: %d", c)
	}
	// плохая раскладка и сохранение без заголовка приложения
	if c, _, _ := deckReq(t, s, "PUT", "/api/deck/layout", `{"rev":1,"groups":[]}`, true); c != 400 {
		t.Fatalf("пустые страницы: %d", c)
	}
	if c, _, _ := deckReq(t, s, "PUT", "/api/deck/layout", strings.Replace(deckLayoutJSON, "%d", "1", 1), false); c != 400 {
		t.Fatalf("без заголовка: %d", c)
	}
	_, b, _ := deckReq(t, s, "GET", "/api/deck/layout", "", false)
	var got struct {
		Saved   bool              `json:"saved"`
		Scenes  []string          `json:"scenes"`
		Palette map[string]string `json:"palette"`
		Icons   map[string]string `json:"icons"`
	}
	_ = json.Unmarshal([]byte(b), &got)
	if !got.Saved || len(got.Scenes) != 2 || len(got.Palette) == 0 || len(got.Icons) == 0 {
		t.Fatalf("layout: %s", b)
	}
	// сброс: возвращается раскладка по умолчанию, а ревизия не откатывается назад
	if c, _, _ := deckReq(t, s, "DELETE", "/api/deck/layout", "", true); c != 200 {
		t.Fatalf("reset: %d", c)
	}
	state()
	if st.Rev != 2 {
		t.Fatalf("ревизия после сброса: %+v", st)
	}
	_, html, _ := deckReq(t, s, "GET", "/deck", "", false)
	if !strings.Contains(html, `data-scene="Chat"`) || !strings.Contains(html, `data-rev="2"`) {
		t.Fatalf("после сброса — плитки по умолчанию: %s", html)
	}
	saveDeck(t, s, 2)
	state()
	if st.Rev != 3 {
		t.Fatalf("новое сохранение продолжает счёт: %+v", st)
	}
}

func TestDeckBucket(t *testing.T) {
	var b deckBucket
	now := time.Unix(100, 0)
	for i := 0; i < deckBurst; i++ {
		if !b.allow(now) {
			t.Fatalf("всплеск %d", i)
		}
	}
	if b.allow(now) {
		t.Fatal("после всплеска отказ")
	}
	if !b.allow(now.Add(time.Second)) {
		t.Fatal("за секунду набегают токены")
	}
}
