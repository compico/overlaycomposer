package server

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fasthttp/websocket"
	"github.com/gofiber/fiber/v3"

	"github.com/compico/overlaycomposer/internal/events"
	"github.com/compico/overlaycomposer/internal/wasmplugins"
)

func TestPluginWSOrigin(t *testing.T) {
	for origin, want := range map[string]bool{
		"": true, "music-application://desktop": true, "http://localhost:8123": true, "HTTP://LOCALHOST:8123": true,
		"https://evil.example": false, "null": false, "music-application://other": false, "http://localhost:8124": false,
	} {
		if got := pluginWSOriginOK(origin, "localhost:8123"); got != want {
			t.Errorf("origin %q: got %v, want %v", origin, got, want)
		}
	}
}

func TestPluginExternalWebSocket(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "echo.wasm"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	mgr := wasmplugins.NewManager(context.Background(), wasmplugins.Options{Engine: srvEngine{handler: func(string, []byte) ([]byte, error) { return nil, nil }}, Dirs: []string{dir}})
	defer mgr.Close()
	mgr.Scan()
	bus := events.New(events.Options{})
	if err := bus.Declare("echo", []string{"now", "commands"}); err != nil {
		t.Fatal(err)
	}
	s := &Server{d: Deps{Wasm: mgr, Events: bus}}
	s.app = fiber.New()
	s.pluginWSRoute()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go s.app.Listener(ln, fiber.ListenConfig{DisableStartupMessage: true})
	defer s.app.Shutdown()
	addr := ln.Addr().String()

	dial := func(path string, hdr http.Header) (*websocket.Conn, *http.Response, error) {
		return websocket.DefaultDialer.Dial("ws://"+addr+path, hdr)
	}
	// чужой сайт, неизвестный плагин
	if _, resp, err := dial("/plugins/echo/ws", http.Header{"Origin": {"https://evil.example"}}); err == nil || resp == nil || resp.StatusCode != 403 {
		t.Fatalf("чужой Origin должен получить 403: %v %v", err, resp)
	}
	if _, resp, err := dial("/plugins/nope/ws", nil); err == nil || resp == nil || resp.StatusCode != 404 {
		t.Fatalf("неизвестный плагин должен получить 404: %v %v", err, resp)
	}
	// не WebSocket
	if resp, err := http.Get("http://" + addr + "/plugins/echo/ws"); err != nil || resp.StatusCode != 426 {
		t.Fatalf("обычный GET: %v %v", err, resp)
	}

	c, _, err := dial("/plugins/echo/ws", http.Header{"Origin": {"music-application://desktop"}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	recv := func() map[string]any {
		t.Helper()
		_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
		_, b, err := c.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		return m
	}
	send := func(v string) {
		t.Helper()
		if err := c.WriteMessage(websocket.TextMessage, []byte(v)); err != nil {
			t.Fatal(err)
		}
	}
	if m := recv(); m["t"] != "hello" || m["plugin"] != "echo" {
		t.Fatalf("hello: %v", m)
	}
	send(`{"t":"ping"}`)
	if m := recv(); m["t"] != "pong" {
		t.Fatalf("pong: %v", m)
	}
	send(`{"t":"sub","topic":"secret"}`)
	if m := recv(); m["ok"] != false || m["error"] != "unknown topic" {
		t.Fatalf("необъявленный топик: %v", m)
	}
	send(`{"t":"sub","topic":"Bad Topic"}`)
	if m := recv(); m["ok"] != false || m["error"] != "invalid topic" {
		t.Fatalf("плохое имя: %v", m)
	}
	send(`not json`) // мусор не роняет соединение
	if err := bus.Publish("echo", "now", json.RawMessage(`{"title":"A"}`), true); err != nil {
		t.Fatal(err)
	}
	send(`{"t":"sub","topic":"now"}`)
	if m := recv(); m["ok"] != true || m["topic"] != "now" {
		t.Fatalf("sub: %v", m)
	}
	if m := recv(); m["t"] != "event" || m["retained"] != true || m["seq"] != float64(1) || m["data"].(map[string]any)["title"] != "A" {
		t.Fatalf("retained: %v", m)
	}
	_ = bus.Publish("echo", "now", json.RawMessage(`{"title":"B"}`), false)
	if m := recv(); m["t"] != "event" || m["seq"] != float64(2) || m["retained"] != nil {
		t.Fatalf("событие: %v", m)
	}
	// повторная подписка не дублирует доставку; unsub останавливает события
	send(`{"t":"sub","topic":"now"}`)
	if m := recv(); m["ok"] != true {
		t.Fatalf("повтор: %v", m)
	}
	send(`{"t":"unsub","topic":"now"}`)
	send(`{"t":"sub","topic":"commands"}`)
	if m := recv(); m["ok"] != true || m["topic"] != "commands" {
		t.Fatalf("commands: %v", m)
	}
	_ = bus.Publish("echo", "now", json.RawMessage(`{"title":"C"}`), false)
	_ = bus.Publish("echo", "commands", json.RawMessage(`{"a":1}`), false)
	if m := recv(); m["topic"] != "commands" {
		t.Fatalf("после unsub пришло событие снятого топика: %v", m)
	}

	// закрытие соединения освобождает слот
	c.Close()
	deadline := time.Now().Add(3 * time.Second)
	for pluginWSConns.Load() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("соединение не освобождено: %d", pluginWSConns.Load())
		}
		time.Sleep(10 * time.Millisecond)
	}
}
