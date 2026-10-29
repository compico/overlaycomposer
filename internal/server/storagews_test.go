package server

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/fasthttp/websocket"
	fws "github.com/gofiber/contrib/v3/websocket"
	"github.com/gofiber/fiber/v3"

	"github.com/compico/overlaycomposer/internal/auth"
	"github.com/compico/overlaycomposer/internal/hub"
	"github.com/compico/overlaycomposer/internal/kv"
)

func TestStorageOverWebSocket(t *testing.T) {
	am, _ := auth.New(&memAuthStore{set: map[string]string{}, devs: map[string]auth.Device{}})
	h := hub.New(stubStore{}, stubObs{}, func() string { return "" }, func() bool { return false })
	s := &Server{d: Deps{Auth: am, Hub: h, KV: kv.New(kv.NewMemory())}}
	s.app = fiber.New()
	s.app.Use(s.originGuard)
	s.app.Use(s.authGuard)
	s.app.Get("/ws", fws.New(s.serveWS))
	s.storageWS()
	s.storageRoutes()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go s.app.Listener(ln, fiber.ListenConfig{DisableStartupMessage: true})
	defer s.app.Shutdown()
	addr := ln.Addr().String()

	recv := func(c *websocket.Conn, typ string) map[string]any {
		t.Helper()
		_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
		for {
			_, b, err := c.ReadMessage()
			if err != nil {
				t.Fatalf("waiting for %s: %v", typ, err)
			}
			var m map[string]any
			_ = json.Unmarshal(b, &m)
			if m["type"] == typ {
				return m
			}
		}
	}
	dial := func() *websocket.Conn {
		c, _, err := websocket.DefaultDialer.Dial("ws://"+addr+"/ws", nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.Close() })
		// Рукопожатие завершается раньше, чем сервер регистрирует соединение в хабе: если сразу отправить запись с другой
		// страницы, рассылка storage:changed не дойдёт до ещё не зарегистрированной (тест был нестабильным). hello шлётся
		// из AddPeer сразу после регистрации — ждём его.
		recv(c, "hello")
		return c
	}
	a, b := dial(), dial()
	rpc := func(c *websocket.Conn, m map[string]any) map[string]any {
		t.Helper()
		m["reqId"] = 1
		if err := c.WriteJSON(m); err != nil {
			t.Fatal(err)
		}
		return recv(c, "storage:result")
	}

	r := rpc(a, map[string]any{"type": "storage:set", "plugin": "tasks", "key": "list", "value": []any{"buy milk"}})
	if r["ok"] != true || r["rev"] != float64(1) {
		t.Fatalf("set: %v", r)
	}
	// другая страница (например, оверлей рядом с редактором) узнаёт об изменении
	ch := recv(b, "storage:changed")
	if ch["plugin"] != "tasks" || ch["key"] != "list" || ch["deleted"] != false || ch["rev"] != float64(1) {
		t.Fatalf("changed: %v", ch)
	}
	if v, _ := ch["value"].([]any); len(v) != 1 || v[0] != "buy milk" {
		t.Fatalf("changed value: %v", ch["value"])
	}

	r = rpc(b, map[string]any{"type": "storage:get", "plugin": "tasks", "key": "list"})
	if r["ok"] != true || r["found"] != true || r["rev"] != float64(1) {
		t.Fatalf("get: %v", r)
	}
	if r = rpc(b, map[string]any{"type": "storage:get", "plugin": "tasks", "key": "none"}); r["ok"] != true || r["found"] != false {
		t.Fatalf("get missing: %v", r)
	}
	// чужое пространство имён пусто
	if r = rpc(b, map[string]any{"type": "storage:get", "plugin": "other", "key": "list"}); r["found"] != false {
		t.Fatalf("namespace leak: %v", r)
	}
	// конфликт ревизий
	r = rpc(b, map[string]any{"type": "storage:set", "plugin": "tasks", "key": "list", "value": 1, "ifRev": 0})
	if r["ok"] != false || r["code"] != "conflict" {
		t.Fatalf("conflict: %v", r)
	}
	if r = rpc(b, map[string]any{"type": "storage:set", "plugin": "tasks", "key": "bad key", "value": 1}); r["code"] != "invalid" {
		t.Fatalf("invalid key: %v", r)
	}
	rpc(a, map[string]any{"type": "storage:set", "plugin": "tasks", "key": "t:2", "value": "b"})
	rpc(a, map[string]any{"type": "storage:set", "plugin": "tasks", "key": "t:1", "value": "a"})
	r = rpc(b, map[string]any{"type": "storage:list", "plugin": "tasks", "prefix": "t:"})
	es, _ := r["entries"].([]any)
	if len(es) != 2 || es[0].(map[string]any)["key"] != "t:1" {
		t.Fatalf("list: %v", r)
	}

	// REST: объём и очистка (с этого компьютера, очистка — только с заголовком приложения)
	do := func(method, path string, hdr map[string]string) (int, string) {
		req, _ := http.NewRequest(method, "http://"+addr+path, nil)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}
	if code, body := do("GET", "/api/plugins/tasks/storage", nil); code != 200 || !strings.Contains(body, `"keys":3`) {
		t.Fatalf("stats: %d %s", code, body)
	}
	if code, _ := do("DELETE", "/api/plugins/tasks/storage", nil); code != 400 {
		t.Fatalf("clear without guard header: %d", code)
	}
	if code, _ := do("DELETE", "/api/plugins/tasks/storage", map[string]string{"X-Overlay-Composer": "1"}); code != 200 {
		t.Fatalf("clear: %d", code)
	}
	if _, body := do("GET", "/api/plugins/tasks/storage", nil); !strings.Contains(body, `"keys":0`) {
		t.Fatalf("after clear: %s", body)
	}
	if code, _ := do("GET", "/api/plugins/bad%20id/storage", nil); code != 400 {
		t.Fatalf("bad plugin id: %d", code)
	}
}
