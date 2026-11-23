package server

import (
	"io"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
)

func TestMixerPocPageIsSelfContainedAndWebSocketNeedsUpgrade(t *testing.T) {
	s := &Server{}
	s.app = fiber.New()
	s.mixerPocRoutes()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go s.app.Listener(ln, fiber.ListenConfig{DisableStartupMessage: true})
	defer s.app.Shutdown()
	base := "http://" + ln.Addr().String()

	get := func(path string) (*http.Response, string) {
		resp, err := http.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp, string(b)
	}
	resp, body := get("/mixer-poc")
	if resp.StatusCode != 200 || !strings.Contains(body, "/api/mixer-poc/ws") || !strings.Contains(resp.Header.Get("Content-Security-Policy"), "default-src 'none'") {
		t.Fatalf("%d %v", resp.StatusCode, resp.Header)
	}
	// Safari не считает 'self' достаточным для ws://: в CSP должен быть явный адрес вместе с портом
	if !strings.Contains(resp.Header.Get("Content-Security-Policy"), "ws://"+ln.Addr().String()) {
		t.Fatalf("в CSP нет ws://адрес:порт: %s", resp.Header.Get("Content-Security-Policy"))
	}
	if strings.Contains(body, "http://") || strings.Contains(body, "https://") {
		t.Fatal("страница не должна ходить на внешние адреса")
	}
	// доступ чужим закрывает общий authGuard (как для /deck), а обычный GET без апгрейда до WebSocket не принимается
	if resp, _ := get("/api/mixer-poc/ws"); resp.StatusCode != fiber.StatusUpgradeRequired {
		t.Fatalf("WebSocket без апгрейда: %d", resp.StatusCode)
	}
}
