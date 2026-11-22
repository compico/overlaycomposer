package server

import (
	"io"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
)

func TestMixerPocIsLocalOnlyAndSelfContained(t *testing.T) {
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

	get := func(path string, hdr map[string]string) (*http.Response, string) {
		req, _ := http.NewRequest("GET", base+path, nil)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp, string(b)
	}
	resp, body := get("/mixer-poc", nil)
	if resp.StatusCode != 200 || !strings.Contains(body, "/api/mixer-poc/ws") || !strings.Contains(resp.Header.Get("Content-Security-Policy"), "default-src 'none'") {
		t.Fatalf("%d %v", resp.StatusCode, resp.Header)
	}
	if strings.Contains(body, "http://") || strings.Contains(body, "https://") && !strings.Contains(body, `"https:"`) {
		t.Fatal("страница не должна ходить на внешние адреса")
	}
	if resp, _ := get("/mixer-poc", map[string]string{"X-Forwarded-For": "10.0.0.5"}); resp.StatusCode != 403 {
		t.Fatalf("через прокси страница недоступна: %d", resp.StatusCode)
	}
	if resp, _ := get("/api/mixer-poc/ws", map[string]string{"X-Forwarded-For": "10.0.0.5"}); resp.StatusCode != 403 {
		t.Fatalf("WebSocket только локально: %d", resp.StatusCode)
	}
}
