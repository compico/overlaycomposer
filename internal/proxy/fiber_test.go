package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"

	"github.com/compico/overlaycomposer/internal/model"
)

// Сквозной тест через настоящий Fiber: заголовки ответа (в т.ч. Content-Type, который раньше склеивался
// с text/plain по умолчанию), cookie, потоковые ответы и Referer-fallback.
func TestFiberEndToEnd(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/w/page", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		http.SetCookie(w, &http.Cookie{Name: "a", Value: "1"})
		http.SetCookie(w, &http.Cookie{Name: "b", Value: "2"})
		io.WriteString(w, `<html><head><link rel="stylesheet" href="/css/b.css"></head></html>`)
	})
	mux.HandleFunc("/css/b.css", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css")
		io.WriteString(w, "html{height:100%}")
	})
	mux.HandleFunc("/js/m.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		io.WriteString(w, "export default 1")
	})
	mux.HandleFunc("/img/a.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		io.WriteString(w, "PNG")
	})
	up := httptest.NewServer(mux)
	defer up.Close()

	src := &model.Source{ID: "s", Type: "iframe", Data: model.SourceData{URL: up.URL + "/w/page", Proxy: true}}
	p := New(func(id string) *model.Source {
		if id == "s" {
			return src
		}
		return nil
	})
	app := fiber.New()
	p.Register(app)
	app.Use(func(c fiber.Ctx) error { return c.Status(404).SendString("our 404") })

	get := func(path string, hdr map[string]string) (*http.Response, string) {
		req := httptest.NewRequest("GET", path, nil)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		return resp, string(b)
	}

	resp, body := get("/proxy/s/w/page", nil)
	if ct := resp.Header.Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("html Content-Type = %q", ct)
	}
	if !strings.Contains(body, `href="/proxy/s/css/b.css"`) {
		t.Errorf("ссылка не переписана: %s", body)
	}
	if ck := resp.Header.Values("Set-Cookie"); len(ck) != 2 {
		t.Errorf("Set-Cookie должен идти двумя строками: %q", ck)
	}

	for _, c := range []struct{ path, ct string }{
		{"/proxy/s/css/b.css", "text/css"},
		{"/proxy/s/js/m.js", "application/javascript"},
		{"/proxy/s/img/a.png", "image/png"}, // потоковый путь
	} {
		resp, _ := get(c.path, nil)
		if ct := resp.Header.Get("Content-Type"); ct != c.ct {
			t.Errorf("%s: Content-Type = %q, want %q", c.path, ct, c.ct)
		}
	}

	// root-relative запрос без префикса — по Referer; зависимость отданного ресурса — по запомненному пути
	resp, body = get("/css/b.css", map[string]string{"Referer": "http://example.com/proxy/s/w/page"})
	if resp.StatusCode != 200 || body != "html{height:100%}" || resp.Header.Get("Content-Type") != "text/css" {
		t.Errorf("fallback: %d %q %q", resp.StatusCode, resp.Header.Get("Content-Type"), body)
	}
	resp, _ = get("/img/a.png", map[string]string{"Referer": "http://example.com/css/b.css"})
	if resp.StatusCode != 200 {
		t.Errorf("цепочка Referer: %d", resp.StatusCode)
	}
	if resp, _ = get("/css/b.css", nil); resp.StatusCode != 404 {
		t.Errorf("без Referer должен быть наш 404, got %d", resp.StatusCode)
	}
}

// Режим «SPA-роутер» источника доходит до страницы: явный выбор важнее списка известных сайтов.
func TestRouterModeReachesPage(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, `<html><head><title>t</title></head><body></body></html>`)
	}))
	defer up.Close()

	for _, c := range []struct{ mode, want string }{
		{model.RouterAuto, `"spa":false`}, // httptest-хост не в списке известных сайтов
		{model.RouterOn, `"spa":true`},
		{model.RouterOff, `"spa":false`},
	} {
		src := &model.Source{ID: "s", Type: "iframe", Data: model.SourceData{URL: up.URL + "/", Proxy: true, Router: c.mode}}
		p := New(func(id string) *model.Source {
			if id == "s" {
				return src
			}
			return nil
		})
		app := fiber.New()
		p.Register(app)
		resp, err := app.Test(httptest.NewRequest("GET", "/proxy/s/", nil))
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		if !strings.Contains(string(b), c.want) {
			t.Errorf("router=%q: want %s in %s", c.mode, c.want, b)
		}
	}
}

// Два источника с одним и тем же путём (/assets/index.js): второй не должен «перетягивать» цепочку первого.
func TestSamePathForTwoSources(t *testing.T) {
	mk := func(name string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/javascript")
			io.WriteString(w, "// "+name+" "+r.URL.Path)
		}))
	}
	a, b := mk("A"), mk("B")
	defer a.Close()
	defer b.Close()
	srcs := map[string]*model.Source{
		"a": {ID: "a", Type: "iframe", Data: model.SourceData{URL: a.URL + "/", Proxy: true}},
		"b": {ID: "b", Type: "iframe", Data: model.SourceData{URL: b.URL + "/", Proxy: true}},
	}
	p := New(func(id string) *model.Source { return srcs[id] })
	app := fiber.New()
	p.Register(app)
	app.Use(func(c fiber.Ctx) error { return c.Status(404).SendString("our 404") })
	get := func(path, ref string) (*http.Response, string) {
		req := httptest.NewRequest("GET", path, nil)
		req.Host = "localhost:8000"
		if ref != "" {
			req.Header.Set("Referer", ref)
		}
		resp, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		bd, _ := io.ReadAll(resp.Body)
		return resp, string(bd)
	}

	// A запрашивает /assets/index.js со своей страницы: отдаётся напрямую и закрепляется за A
	if resp, body := get("/assets/index.js", "http://localhost:8000/proxy/a/page"); resp.StatusCode != 200 || !strings.Contains(body, "A /assets/index.js") {
		t.Fatalf("A: %d %q", resp.StatusCode, body)
	}
	// B просит тот же путь: уводим под /proxy/b/, а не отдаём и не переписываем владельца
	resp, _ := get("/assets/index.js?v=1", "http://localhost:8000/proxy/b/page")
	if resp.StatusCode != 307 || resp.Header.Get("Location") != "/proxy/b/assets/index.js?v=1" {
		t.Fatalf("B должен получить редирект на свой префикс, got %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	// после редиректа файл B отдаётся прокси B, а зависимости файла A по-прежнему находятся как A
	if _, body := get("/proxy/b/assets/index.js", ""); !strings.Contains(body, "B /assets/index.js") {
		t.Fatalf("B за префиксом: %q", body)
	}
	if _, body := get("/assets/dep.js", "http://localhost:8000/assets/index.js"); !strings.Contains(body, "A /assets/dep.js") {
		t.Fatalf("зависимость файла A ушла не к A: %q", body)
	}
	// sad path: POST не редиректим (тело потерялось бы) — он уходит к источнику из Referer
	req := httptest.NewRequest("POST", "/assets/index.js", strings.NewReader("x"))
	req.Host = "localhost:8000"
	req.Header.Set("Referer", "http://localhost:8000/proxy/b/page")
	if r, err := app.Test(req); err != nil || r.StatusCode == 307 {
		t.Fatalf("POST: %v %v", err, r)
	}
}
