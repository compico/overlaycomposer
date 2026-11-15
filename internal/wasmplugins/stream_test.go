package wasmplugins

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fasthttp/websocket"

	"github.com/compico/overlaycomposer/internal/events"
	"github.com/compico/overlaycomposer/internal/plugins"
)

const tosuFrame = `{"beatmap":{"checksum":"abc","time":{"live":%d},"extra":{"big":[1,2,3]}},"directPath":{"beatmapFile":"1 a\\b.osu"},"play":{"mods":{"name":"HD"}},"other":"x"}`

var streamFields = map[string]string{"file": "directPath.beatmapFile", "time": "beatmap.time.live", "mods": "play.mods.name", "missing": "play.nothing.here"}

func TestExtractFields(t *testing.T) {
	out, err := extractFields([]byte(strings.Replace(tosuFrame, "%d", "1500", 1)), streamFields)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"file": `"1 a\\b.osu"`, "time": `1500`, "mods": `"HD"`, "missing": `null`}
	if len(got) != len(want) {
		t.Fatalf("%s", out)
	}
	for k, v := range want {
		if string(got[k]) != v {
			t.Errorf("%s: %s, ждали %s", k, got[k], v)
		}
	}
	for _, bad := range []string{``, `[1]`, `"x"`, `{`} {
		if _, err := extractFields([]byte(bad), streamFields); err == nil {
			t.Errorf("%q: ждали ошибку", bad)
		}
	}
	// Путь через не-объект даёт null, а не ошибку.
	out, err = extractFields([]byte(`{"a":5}`), map[string]string{"x": "a.b"})
	if err != nil || string(out) != `{"x":null}` {
		t.Fatalf("%s %v", out, err)
	}
}

func TestStreamMetaValidation(t *testing.T) {
	good := func() Meta {
		m := baseMeta("s")
		m.Permissions = []string{PermNetLocal}
		m.Events = []EventDecl{{ID: "live"}}
		m.NetLocal = []NetLocalTarget{{Target: "127.0.0.1:24050", Streams: []NetLocalStream{{Path: "/websocket/v2", Topic: "live", Fields: map[string]string{"a": "b.c"}}}}}
		return m
	}
	if err := (func() error { m := good(); return m.Validate() })(); err != nil {
		t.Fatal(err)
	}
	for name, mut := range map[string]func(*Meta){
		"топик не объявлен": func(m *Meta) { m.Events = nil },
		"путь с ..":         func(m *Meta) { m.NetLocal[0].Streams[0].Path = "/a/../b" },
		"путь без /":        func(m *Meta) { m.NetLocal[0].Streams[0].Path = "ws" },
		"путь с запросом":   func(m *Meta) { m.NetLocal[0].Streams[0].Path = "/ws?x=1" },
		"нет полей":         func(m *Meta) { m.NetLocal[0].Streams[0].Fields = nil },
		"плохое имя поля":   func(m *Meta) { m.NetLocal[0].Streams[0].Fields = map[string]string{"a-b": "x"} },
		"плохой путь поля":  func(m *Meta) { m.NetLocal[0].Streams[0].Fields = map[string]string{"a": "x..y"} },
		"интервал мал":      func(m *Meta) { m.NetLocal[0].Streams[0].MinIntervalMs = 10 },
		"интервал велик":    func(m *Meta) { m.NetLocal[0].Streams[0].MinIntervalMs = 999999 },
		"слишком много полей": func(m *Meta) {
			f := map[string]string{}
			for i := 0; i < maxStreamFields+1; i++ {
				f[string(rune('a'+i))] = "x"
			}
			m.NetLocal[0].Streams[0].Fields = f
		},
		"слишком много потоков": func(m *Meta) {
			s := m.NetLocal[0].Streams[0]
			m.NetLocal[0].Streams = []NetLocalStream{s, s, s}
		},
	} {
		m := good()
		mut(&m)
		if err := m.Validate(); err == nil {
			t.Errorf("%s: ждали ошибку", name)
		}
	}
}

// tosuServer — поддельный tosu: шлёт кадры каждые 10 мс и запоминает заголовок Origin.
type tosuServer struct {
	srv     *httptest.Server
	mu      sync.Mutex
	origins []string
	conns   atomic.Int32
	frames  atomic.Int32
}

func newTosuServer(t *testing.T) *tosuServer {
	ts := &tosuServer{}
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	ts.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/websocket/v2" {
			http.NotFound(w, r)
			return
		}
		ts.mu.Lock()
		ts.origins = append(ts.origins, r.Header.Get("Origin"))
		ts.mu.Unlock()
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		ts.conns.Add(1)
		defer ts.conns.Add(-1)
		defer c.Close()
		go func() { // читаем, чтобы заметить закрытие
			for {
				if _, _, err := c.ReadMessage(); err != nil {
					return
				}
			}
		}()
		for i := 0; ; i++ {
			msg := strings.Replace(tosuFrame, "%d", itoa(i*10), 1)
			if err := c.WriteMessage(websocket.TextMessage, []byte(msg)); err != nil {
				return
			}
			ts.frames.Add(1)
			time.Sleep(10 * time.Millisecond)
		}
	}))
	t.Cleanup(ts.srv.Close)
	return ts
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func streamPlugin(t *testing.T, ts *tosuServer, gap int, grants ...string) (*Plugin, *events.Broker) {
	m := baseMeta("tosu-live")
	m.Permissions = []string{PermNetLocal}
	m.Events = []EventDecl{{ID: "live"}}
	m.NetLocal = []NetLocalTarget{{Target: hostOf(ts.srv), Streams: []NetLocalStream{{Path: "/websocket/v2", Topic: "live", Fields: streamFields, MinIntervalMs: gap}}}}
	bus := events.New(events.Options{})
	mod := &fakeModule{meta: m}
	p := newPlugin(context.Background(), Services{Log: quietLog(), Events: bus}, mod, m, "x.wasm", "sha", nil, grants)
	if err := p.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Stop)
	return p, bus
}

func TestStreamRelaysFieldsWithoutOriginAndThrottles(t *testing.T) {
	ts := newTosuServer(t)
	_, bus := streamPlugin(t, ts, 100, PermNetLocal)

	// Пока подписчиков нет, к tosu не подключаемся.
	time.Sleep(1500 * time.Millisecond)
	if ts.conns.Load() != 0 || ts.frames.Load() != 0 {
		t.Fatal("подключились к tosu без подписчиков")
	}

	sub, err := bus.Subscribe("tosu-live", "live", 64)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	var got []events.Event
	deadline := time.After(5 * time.Second)
	for len(got) < 3 {
		select {
		case ev := <-sub.C:
			got = append(got, ev)
		case <-deadline:
			t.Fatalf("получили %d событий", len(got))
		}
	}
	var d struct {
		File string `json:"file"`
		Time int    `json:"time"`
		Mods string `json:"mods"`
	}
	if err := json.Unmarshal(got[0].Data, &d); err != nil || d.File != `1 a\b.osu` || d.Mods != "HD" {
		t.Fatalf("данные: %s %v", got[0].Data, err)
	}
	ts.mu.Lock()
	origins := append([]string(nil), ts.origins...)
	ts.mu.Unlock()
	if len(origins) == 0 || origins[0] != "" {
		t.Fatalf("Origin должен отсутствовать: %q", origins)
	}
	// Кадры идут каждые 10 мс, интервал 100 мс: событий заметно меньше кадров.
	if int(ts.frames.Load()) < 3*5 && len(got) >= 3 {
		t.Logf("кадров %d", ts.frames.Load())
	}
	if frames := int(ts.frames.Load()); frames > 0 && len(got)*5 > frames+5 {
		t.Fatalf("прореживания нет: %d событий на %d кадров", len(got), frames)
	}

	// Последний подписчик ушёл — хост закрывает соединение.
	sub.Close()
	waitFor(t, "закрытие соединения без подписчиков", func() bool { return ts.conns.Load() == 0 })
}

func TestStreamNeedsGrant(t *testing.T) {
	ts := newTosuServer(t)
	_, bus := streamPlugin(t, ts, 100) // net.local не выдан
	sub, err := bus.Subscribe("tosu-live", "live", 8)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	time.Sleep(2500 * time.Millisecond)
	if ts.conns.Load() != 0 || ts.frames.Load() != 0 {
		t.Fatal("без разрешения net.local хост не должен подключаться")
	}
}

func TestWidgetSettingsValidation(t *testing.T) {
	good := func() Meta {
		m := baseMeta("w")
		m.Widgets = []Widget{{ID: "g", Name: "G", Entry: "index.html", Settings: []plugins.Setting{
			{Key: "style", Type: plugins.TSelect, Options: []plugins.Option{{Value: "a", Label: "A"}, {Value: "b", Label: "B"}}, Default: "a"},
			{Key: "size", Label: "Size", Type: plugins.TNumber},
		}}}
		return m
	}
	m := good()
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
	if m.Widgets[0].Settings[0].Label != "style" {
		t.Fatalf("пустая подпись должна заполниться ключом: %q", m.Widgets[0].Settings[0].Label)
	}
	for name, mut := range map[string]func(*Meta){
		"select без вариантов": func(m *Meta) { m.Widgets[0].Settings[0].Options = nil },
		"неизвестный тип":      func(m *Meta) { m.Widgets[0].Settings[1].Type = "pie" },
		"повтор ключа":         func(m *Meta) { m.Widgets[0].Settings[1].Key = "style" },
		"плохой ключ":          func(m *Meta) { m.Widgets[0].Settings[1].Key = "1x" },
		"слишком много": func(m *Meta) {
			for i := 0; i < maxWidgetSettings; i++ {
				m.Widgets[0].Settings = append(m.Widgets[0].Settings, plugins.Setting{Key: "k" + itoa(i), Type: plugins.TString})
			}
		},
	} {
		m := good()
		mut(&m)
		if err := m.Validate(); err == nil {
			t.Errorf("%s: ждали ошибку", name)
		}
	}
}
