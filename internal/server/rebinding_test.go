package server

import (
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
)

func TestLoopbackHost(t *testing.T) {
	for host, want := range map[string]bool{
		"localhost": true, "localhost:8000": true, "LOCALHOST.": true, "app.localhost:8000": true,
		"127.0.0.1:8000": true, "127.0.0.1": true, "[::1]:8000": true, "[::1]": true, "192.168.1.5:8000": true,
		"evil.example": false, "evil.example:8000": false, "127.0.0.1.evil.example": false, "": false,
		"localhost.evil.example": false,
	} {
		if got := loopbackHost(host); got != want {
			t.Errorf("loopbackHost(%q) = %v, want %v", host, got, want)
		}
	}
}

// Реальный loopback-сокет: именно так выглядит DNS rebinding (соединение с 127.0.0.1, но Host — чужой домен).
func TestDNSRebindingIsRejected(t *testing.T) {
	am, _ := auth.New(&memAuthStore{set: map[string]string{}, devs: map[string]auth.Device{}})
	s := &Server{d: Deps{Auth: am}}
	s.app = fiber.New()
	s.app.Use(s.originGuard, s.authGuard)
	s.app.Get("/api/state", func(c fiber.Ctx) error { return c.SendString("secret") })
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go s.app.Listener(ln, fiber.ListenConfig{DisableStartupMessage: true})
	defer s.app.Shutdown()

	get := func(host string) (int, string) {
		req, _ := http.NewRequest("GET", "http://"+ln.Addr().String()+"/api/state", nil)
		req.Host = host
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b := make([]byte, 64)
		n, _ := resp.Body.Read(b)
		return resp.StatusCode, string(b[:n])
	}
	if code, body := get(ln.Addr().String()); code != 200 || body != "secret" {
		t.Fatalf("own address: %d %q", code, body)
	}
	if code, body := get("localhost:" + strings.Split(ln.Addr().String(), ":")[1]); code != 200 || body != "secret" {
		t.Fatalf("localhost: %d %q", code, body)
	}
	// sad path: чужое имя в Host при соединении с loopback
	if code, body := get("rebind.evil.example:8000"); code != 403 || strings.Contains(body, "secret") {
		t.Fatalf("rebinding: %d %q", code, body)
	}
}

// sad path: огромное сообщение по WebSocket обрывает соединение, а не читается целиком.
func TestWSMessageLimit(t *testing.T) {
	am, _ := auth.New(&memAuthStore{set: map[string]string{}, devs: map[string]auth.Device{}})
	h := hub.New(stubStore{}, stubObs{}, func() string { return "" }, func() bool { return false })
	s := &Server{d: Deps{Auth: am, Hub: h}}
	s.app = fiber.New()
	s.app.Get("/ws", fws.New(s.serveWS))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go s.app.Listener(ln, fiber.ListenConfig{DisableStartupMessage: true})
	defer s.app.Shutdown()

	c, _, err := websocket.DefaultDialer.Dial("ws://"+ln.Addr().String()+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	big := []byte(strings.Repeat("a", maxWSMessage+1024))
	_ = c.WriteMessage(websocket.TextMessage, big)
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		if _, _, err := c.ReadMessage(); err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				t.Fatal("соединение не закрыто после слишком большого сообщения")
			}
			return // закрыто сервером
		}
	}
}
