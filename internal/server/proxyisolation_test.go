package server

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/compico/overlaycomposer/internal/auth"
	"github.com/compico/overlaycomposer/internal/config"
	"github.com/compico/overlaycomposer/internal/hub"
	"github.com/compico/overlaycomposer/internal/kv"
	"github.com/compico/overlaycomposer/internal/model"
	"github.com/compico/overlaycomposer/internal/proxy"
)

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// newDeps — минимальный набор зависимостей, чтобы New() зарегистрировал все маршруты.
func newDeps(px *proxy.Proxy, am *auth.Manager, running config.Config) Deps {
	h := hub.New(stubStore{}, stubObs{}, func() string { return "" }, func() bool { return false })
	return Deps{Proxy: px, Auth: am, Hub: h, KV: kv.New(kv.NewMemory()), Running: running}
}

func TestProxyPortFor(t *testing.T) {
	for _, c := range []struct {
		port, proxy, want int
	}{
		{8000, 0, 8001}, {8000, 9000, 9000}, {8000, -1, 0}, // -1 — изоляция выключена
		{8000, 8000, 0}, {65535, 0, 0}, {0, 0, 0}, // тот же порт / за пределами / без порта
	} {
		if got := proxyPortFor(config.Config{Port: c.port, ProxyPort: c.proxy}); got != c.want {
			t.Errorf("port=%d proxyPort=%d: %d, want %d", c.port, c.proxy, got, c.want)
		}
	}
}

func TestWithProxyPortInjectsMeta(t *testing.T) {
	s := &Server{proxyPort: 8001}
	out := string(s.withProxyPort([]byte("<html><head><title>x</title></head><body></body></html>")))
	if !strings.Contains(out, `<meta name="oc-proxy-port" content="8001"></head>`) {
		t.Fatalf("%s", out)
	}
	in := []byte("<html><head></head></html>")
	if got := (&Server{}).withProxyPort(in); string(got) != string(in) {
		t.Fatalf("без изоляции страница не меняется: %s", got)
	}
	if got := s.withProxyPort([]byte("no head")); string(got) != "no head" {
		t.Fatalf("без </head> страница не меняется: %s", got)
	}
}

// Сквозной тест: приложение и прокси слушают разные порты. Сайт источника открывается только на порту прокси, а на
// порту прокси нет ни API приложения, ни WebSocket, ни страниц.
func TestProxyIsolation(t *testing.T) {
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintf(w, "site:%s cookie=%q", r.URL.Path, r.Header.Get("Cookie"))
	}))
	defer site.Close()
	src := &model.Source{ID: "w", Type: "iframe", Data: model.SourceData{URL: site.URL + "/", Proxy: true}}
	px := proxy.New(func(id string) *model.Source {
		if id == "w" {
			return src
		}
		return nil
	})
	am, _ := auth.New(&memAuthStore{set: map[string]string{}, devs: map[string]auth.Device{}})

	mainPort, proxyPort := freePort(t), freePort(t)
	s := New(newDeps(px, am, config.Config{Host: "127.0.0.1", Port: mainPort, ProxyPort: proxyPort}))
	if s.proxyLn == nil || s.proxyPort != proxyPort {
		t.Fatalf("прокси не изолирован: %+v", s.proxyPort)
	}
	if _, err := s.Listen(); err != nil {
		t.Fatal(err)
	}
	defer s.Shutdown()

	get := func(port int, path string, hdr map[string]string) (int, string) {
		req, _ := http.NewRequest("GET", fmt.Sprintf("http://127.0.0.1:%d%s", port, path), nil)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	// на порту прокси сайт доступен, cookie приложения до него не доходит, остальные — доходят
	code, body := get(proxyPort, "/proxy/w/page", map[string]string{"Cookie": "sid=1; oc_token=SECRET"})
	if code != 200 || !strings.Contains(body, "site:/page") || strings.Contains(body, "SECRET") || !strings.Contains(body, "sid=1") {
		t.Fatalf("прокси: %d %q", code, body)
	}
	// Referer-fallback работает на порту прокси
	code, body = get(proxyPort, "/assets/a.js", map[string]string{"Referer": fmt.Sprintf("http://127.0.0.1:%d/proxy/w/page", proxyPort)})
	if code != 200 || !strings.Contains(body, "site:/assets/a.js") {
		t.Fatalf("fallback: %d %q", code, body)
	}

	// sad path: на основном порту сайты источников не отдаются…
	if code, body = get(mainPort, "/proxy/w/page", nil); code == 200 || strings.Contains(body, "site:") {
		t.Fatalf("основной порт отдал сайт источника: %d %q", code, body)
	}
	// …а на порту прокси нет API приложения — даже с Referer страницы прокси
	for _, p := range []string{"/api/state", "/api/devices", "/api/layouts", "/ws", "/editor", "/pair", "/api/twitch/accounts"} {
		code, body := get(proxyPort, p, map[string]string{"Referer": fmt.Sprintf("http://127.0.0.1:%d/proxy/w/page", proxyPort)})
		// с Referer страницы прокси запрос уходит на сайт источника (его собственные пути); страницей приложения он быть не может
		if code == 200 && !strings.Contains(body, "site:") {
			t.Errorf("%s на порту прокси отдал страницу приложения: %d %q", p, code, body)
		}
	}
	// без Referer такие пути — просто 404
	for _, p := range []string{"/api/state", "/api/devices", "/ws", "/editor"} {
		if code, _ := get(proxyPort, p, nil); code != 404 {
			t.Errorf("%s без Referer: %d, want 404", p, code)
		}
	}
	// POST со страницы прокси на API приложения (другой origin) режет originGuard
	req, _ := http.NewRequest("POST", fmt.Sprintf("http://127.0.0.1:%d/api/sources", mainPort), strings.NewReader("{}"))
	req.Header.Set("Origin", fmt.Sprintf("http://127.0.0.1:%d", proxyPort))
	req.Header.Set("X-Overlay-Composer", "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Errorf("кросс-origin POST с порта прокси: %d, want 403", resp.StatusCode)
	}
}

// Порт прокси занят — приложение работает как раньше: прокси на основном порту.
func TestProxyFallsBackWhenPortIsTaken(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	busyPort := busy.Addr().(*net.TCPAddr).Port

	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "site:", r.URL.Path) }))
	defer site.Close()
	src := &model.Source{ID: "w", Type: "iframe", Data: model.SourceData{URL: site.URL + "/", Proxy: true}}
	px := proxy.New(func(id string) *model.Source {
		if id == "w" {
			return src
		}
		return nil
	})
	mainPort := freePort(t)
	s := New(newDeps(px, nil, config.Config{Host: "127.0.0.1", Port: mainPort, ProxyPort: busyPort}))
	if s.proxyLn != nil || s.proxyPort != 0 {
		t.Fatalf("порт занят — изоляции быть не должно: %d", s.proxyPort)
	}
	if string(s.withProxyPort([]byte("<head></head>"))) != "<head></head>" {
		t.Fatal("страница не должна ссылаться на порт прокси")
	}
	if _, err := s.Listen(); err != nil {
		t.Fatal(err)
	}
	defer s.Shutdown()
	// на основном порту прокси работает, как раньше
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/proxy/w/page", mainPort))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.Contains(string(b), "site:/page") {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
}
