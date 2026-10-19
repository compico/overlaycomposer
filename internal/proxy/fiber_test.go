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
