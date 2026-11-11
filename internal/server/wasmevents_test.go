package server

import (
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/fasthttp/websocket"
	fws "github.com/gofiber/contrib/v3/websocket"
	"github.com/gofiber/fiber/v3"

	"github.com/compico/overlaycomposer/internal/auth"
	"github.com/compico/overlaycomposer/internal/events"
	"github.com/compico/overlaycomposer/internal/hub"
)

func TestPluginEventsOverWebSocket(t *testing.T) {
	am, _ := auth.New(&memAuthStore{set: map[string]string{}, devs: map[string]auth.Device{}})
	h := hub.New(stubStore{}, stubObs{}, func() string { return "" }, func() bool { return false })
	bus := events.New(events.Options{})
	if err := bus.Declare("music", []string{"now", "commands"}); err != nil {
		t.Fatal(err)
	}
	s := &Server{d: Deps{Auth: am, Hub: h, Events: bus}}
	// кто может обращаться к плагину, решает authorizePlugin (проверяется отдельно): здесь пускаем только источник «ok»
	s.pluginAuth = func(plugin, source string) string {
		if plugin == "music" && source == "ok" {
			return ""
		}
		return "unknown widget"
	}
	s.app = fiber.New()
	s.app.Use(s.originGuard)
	s.app.Use(s.authGuard)
	s.app.Get("/ws", fws.New(s.serveWS))
	s.pluginWS()
	h.OnPeerGone(s.dropPeerSubs)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go s.app.Listener(ln, fiber.ListenConfig{DisableStartupMessage: true})
	defer s.app.Shutdown()

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
	c, _, err := websocket.DefaultDialer.Dial("ws://"+ln.Addr().String()+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	recv(c, "hello")
	sub := func(plugin, source, topic string, id int) map[string]any {
		t.Helper()
		if err := c.WriteJSON(map[string]any{"type": "plugin:sub", "reqId": id, "plugin": plugin, "source": source, "topic": topic}); err != nil {
			t.Fatal(err)
		}
		return recv(c, "plugin:sub:result")
	}

	// отказы: чужой источник, необъявленный и некорректный топик, другой плагин
	if r := sub("music", "nope", "now", 1); r["ok"] == true || r["error"] != "unknown widget" {
		t.Fatalf("чужой источник: %v", r)
	}
	if r := sub("music", "ok", "secret", 2); r["ok"] == true || r["error"] != "unknown topic" {
		t.Fatalf("необъявленный топик: %v", r)
	}
	if r := sub("music", "ok", "Bad Topic", 3); r["ok"] == true || r["error"] != "invalid topic" {
		t.Fatalf("плохое имя: %v", r)
	}
	if r := sub("other", "ok", "now", 4); r["ok"] == true {
		t.Fatalf("другой плагин: %v", r)
	}

	// retained-значение приходит сразу после подписки, свежие события — следом
	if err := bus.Publish("music", "now", json.RawMessage(`{"title":"A"}`), true); err != nil {
		t.Fatal(err)
	}
	if r := sub("music", "ok", "now", 5); r["ok"] != true || r["reqId"] != float64(5) {
		t.Fatalf("подписка: %v", r)
	}
	ev := recv(c, "plugin:event")
	if ev["plugin"] != "music" || ev["topic"] != "now" || ev["retained"] != true || ev["seq"] != float64(1) {
		t.Fatalf("retained: %v", ev)
	}
	if err := bus.Publish("music", "now", json.RawMessage(`{"title":"B"}`), true); err != nil {
		t.Fatal(err)
	}
	ev = recv(c, "plugin:event")
	if ev["retained"] != nil || ev["seq"] != float64(2) || ev["data"].(map[string]any)["title"] != "B" {
		t.Fatalf("свежее событие: %v", ev)
	}
	// повторная подписка подтверждается и не дублирует доставку
	if r := sub("music", "ok", "now", 6); r["ok"] != true {
		t.Fatalf("повтор: %v", r)
	}
	_ = bus.Publish("music", "now", json.RawMessage(`{"title":"C"}`), false)
	if ev = recv(c, "plugin:event"); ev["seq"] != float64(3) {
		t.Fatalf("после повтора: %v", ev)
	}

	// unsub: события перестают приходить (проверяем событием другого топика, на которое подписались позже)
	if err := c.WriteJSON(map[string]any{"type": "plugin:unsub", "plugin": "music", "topic": "now"}); err != nil {
		t.Fatal(err)
	}
	if r := sub("music", "ok", "commands", 7); r["ok"] != true {
		t.Fatalf("commands: %v", r)
	}
	_ = bus.Publish("music", "now", json.RawMessage(`{"title":"D"}`), false)
	_ = bus.Publish("music", "commands", json.RawMessage(`{"action":"skip"}`), false)
	if ev = recv(c, "plugin:event"); ev["topic"] != "commands" {
		t.Fatalf("после unsub пришло событие снятого топика: %v", ev)
	}

	// закрытое соединение освобождает подписки
	c.Close()
	deadline := time.Now().Add(3 * time.Second)
	for {
		s.evMu.Lock()
		n := len(s.evPeers)
		s.evMu.Unlock()
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("подписки закрытого соединения не сняты: %d", n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
