package server

import (
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"

	"github.com/compico/overlaycomposer/internal/auth"
)

func TestPluginAPIPath(t *testing.T) {
	for p, want := range map[string]bool{
		"/plugins/music/api": true, "/plugins/music/api/": true, "/plugins/music/api/nowplaying": true, "/plugins/a-b.c/api/x/y": true,
		"/plugins/music/apix": false, "/plugins/music/api2/x": false, "/plugins/music/ui/api/x": false, "/plugins/api": false,
		"/plugins//api/x": false, "/plugins/": false, "/plugins": false, "/api/settings": false, "/ws": false, "/": false,
		"/plugins/music/widget/api": false, "/Plugins/music/api/x": false,
	} {
		if got := pluginAPIPath(p); got != want {
			t.Errorf("pluginAPIPath(%q) = %v, want %v", p, got, want)
		}
	}
}

func TestTrustedPluginOriginIsExact(t *testing.T) {
	if !trustedPluginOrigin("music-application://desktop") {
		t.Fatal("origin окна Яндекс Музыки должен быть доверенным")
	}
	for _, o := range []string{"", "null", "https://evil.example", "music-application://desktop.evil.example", "music-application://desktop:1",
		"music-application://desktop/", "MUSIC-APPLICATION://DESKTOP", "music-application://", "http://127.0.0.1:8000", " music-application://desktop"} {
		if trustedPluginOrigin(o) {
			t.Errorf("origin %q не должен быть доверенным", o)
		}
	}
}

// Реальный loopback-сокет с теми же защитами, что в приложении: pluginAPIOrigin → originGuard → authGuard.
func TestPluginAPITrustedOrigin(t *testing.T) {
	am, _ := auth.New(&memAuthStore{set: map[string]string{}, devs: map[string]auth.Device{}})
	s := &Server{d: Deps{Auth: am}}
	s.app = fiber.New()
	s.app.Use(s.pluginAPIOrigin, s.originGuard, s.authGuard)
	ok := func(c fiber.Ctx) error { return c.SendString("done") }
	s.app.All("/plugins/:id/api/*", ok)
	s.app.All("/plugins/:id/ui/*", ok)
	s.app.All("/api/*", ok)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go s.app.Listener(ln, fiber.ListenConfig{DisableStartupMessage: true})
	defer s.app.Shutdown()

	do := func(method, path, origin, host string) (*http.Response, string) {
		t.Helper()
		req, _ := http.NewRequest(method, "http://"+ln.Addr().String()+path, strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		if host != "" {
			req.Host = host
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b := make([]byte, 16)
		n, _ := resp.Body.Read(b)
		return resp, string(b[:n])
	}
	const yam = "music-application://desktop"

	// preflight доверенного origin: 204 и разрешения для POST с JSON
	resp, _ := do("OPTIONS", "/plugins/music/api/nowplaying", yam, "")
	h := resp.Header
	if resp.StatusCode != 204 || h.Get("Access-Control-Allow-Origin") != yam || !strings.Contains(h.Get("Access-Control-Allow-Methods"), "POST") ||
		!strings.EqualFold(h.Get("Access-Control-Allow-Headers"), "Content-Type") || h.Get("Vary") == "" {
		t.Fatalf("preflight: %d %v", resp.StatusCode, h)
	}
	// сам POST проходит originGuard и читается страницей
	resp, body := do("POST", "/plugins/music/api/nowplaying", yam, "")
	if resp.StatusCode != 200 || body != "done" || resp.Header.Get("Access-Control-Allow-Origin") != yam {
		t.Fatalf("trusted POST: %d %q %v", resp.StatusCode, body, resp.Header)
	}
	// своя страница OC работает как раньше
	if resp, _ = do("POST", "/plugins/music/api/x", "http://"+ln.Addr().String(), ""); resp.StatusCode != 200 {
		t.Fatalf("same-origin POST: %d", resp.StatusCode)
	}

	// sad paths: чужие origin по-прежнему режутся и CORS не получают
	for _, o := range []string{"https://evil.example", "null", "music-application://desktop.evil.example", "MUSIC-APPLICATION://DESKTOP"} {
		resp, body := do("POST", "/plugins/music/api/control", o, "")
		if resp.StatusCode != 403 || strings.Contains(body, "done") || resp.Header.Get("Access-Control-Allow-Origin") != "" {
			t.Errorf("origin %q: %d %q %v", o, resp.StatusCode, body, resp.Header)
		}
		if resp, _ = do("OPTIONS", "/plugins/music/api/control", o, ""); resp.Header.Get("Access-Control-Allow-Origin") != "" {
			t.Errorf("preflight от %q получил CORS", o)
		}
	}
	// доверие не выходит за маршруты плагинов: API приложения и «похожие» пути закрыты
	for _, p := range []string{"/api/settings", "/api/wasmplugins/music/grants", "/plugins/music/apix/x", "/plugins/music/ui/api/x", "/plugins/api", "/plugins/music/ui/index.html"} {
		resp, body := do("POST", p, yam, "")
		if resp.StatusCode != 403 || strings.Contains(body, "done") || resp.Header.Get("Access-Control-Allow-Origin") != "" {
			t.Errorf("%s с доверенным origin: %d %q %v", p, resp.StatusCode, body, resp.Header)
		}
		if resp, _ = do("OPTIONS", p, yam, ""); resp.Header.Get("Access-Control-Allow-Origin") != "" {
			t.Errorf("preflight %s получил CORS", p)
		}
	}
	// DNS rebinding не обходится доверенным origin: authGuard всё равно требует локальный Host
	resp, body = do("POST", "/plugins/music/api/nowplaying", yam, "rebind.evil.example:8000")
	if resp.StatusCode != 403 || strings.Contains(body, "done") {
		t.Fatalf("rebinding с доверенным origin: %d %q", resp.StatusCode, body)
	}
}
