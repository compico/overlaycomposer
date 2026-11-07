package server

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/gofiber/fiber/v3"

	"github.com/compico/overlaycomposer/internal/paths"
	"github.com/compico/overlaycomposer/internal/plugins"
)

func TestInstallLocalWidgetEndpoint(t *testing.T) {
	dir := t.TempDir()
	reg := plugins.New(fstest.MapFS{}, os.DirFS(dir))
	s := &Server{d: Deps{Paths: paths.Paths{PluginsDir: dir}, Plugins: reg}}
	s.app = fiber.New()
	s.app.Post("/api/plugins/local", s.installLocalWidget)

	post := func(name string, files map[string]string) (int, map[string]any) {
		var buf bytes.Buffer
		w := multipart.NewWriter(&buf)
		_ = w.WriteField("name", name)
		for p, data := range files {
			part, _ := w.CreateFormFile("files", filepath.Base(p))
			_, _ = part.Write([]byte(data))
			_ = w.WriteField("paths", p)
		}
		_ = w.Close()
		req := httptest.NewRequest("POST", "/api/plugins/local", &buf)
		req.Header.Set("Content-Type", w.FormDataContentType())
		resp, err := s.app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		out, _ := io.ReadAll(resp.Body)
		var m map[string]any
		_ = json.Unmarshal(out, &m)
		return resp.StatusCode, m
	}

	code, m := post("Alert", map[string]string{"alert.html": "<p>hi</p>", "css/a.css": "p{}"})
	if code != 200 || m["id"] != "local-alert" {
		t.Fatalf("загрузка: %d %v", code, m)
	}
	if _, ok := reg.Get("local-alert"); !ok {
		t.Fatal("плагин должен появиться в реестре сразу")
	}
	if _, err := os.Stat(filepath.Join(dir, "local-alert", "css", "a.css")); err != nil {
		t.Fatalf("файл в подпапке: %v", err)
	}

	// sad path: путь наружу, нет HTML, пустое имя — 400, на диске ничего нового
	before, _ := os.ReadDir(dir)
	for name, c := range map[string]struct {
		name  string
		files map[string]string
	}{
		"путь наружу": {"X", map[string]string{"a.html": "x", "../evil.html": "y"}},
		"нет html":    {"X", map[string]string{"a.css": "x"}},
		"пустое имя":  {" ", map[string]string{"a.html": "x"}},
	} {
		if code, _ := post(c.name, c.files); code != 400 {
			t.Errorf("%s: код %d, ожидался 400", name, code)
		}
	}
	if after, _ := os.ReadDir(dir); len(after) != len(before) {
		t.Fatalf("при отказе на диске появились файлы: %v", after)
	}

	// не multipart
	req := httptest.NewRequest("POST", "/api/plugins/local", bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.app.Test(req)
	if err != nil || resp.StatusCode != 400 {
		t.Fatalf("JSON вместо multipart: %v %v", err, resp)
	}
}
