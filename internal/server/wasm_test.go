package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/gofiber/fiber/v3"

	"github.com/compico/overlaycomposer/internal/config"
	"github.com/compico/overlaycomposer/internal/plugins"
	"github.com/compico/overlaycomposer/internal/wasmassets"
	"github.com/compico/overlaycomposer/internal/wasmplugins"
)

// Минимальный движок: «wasm» не нужен, поведение плагина задаёт функция. Настоящий wasm проверяет пакет wasmplugins.
type srvEngine struct {
	handler func(name string, arg []byte) ([]byte, error)
}

func (e srvEngine) Compile(context.Context, []byte) (wasmplugins.Module, error) {
	return srvModule(e), nil
}
func (srvEngine) Close(context.Context) error { return nil }

type srvModule struct {
	handler func(string, []byte) ([]byte, error)
}

func (m srvModule) New(context.Context, wasmplugins.HostCall, io.Writer) (wasmplugins.Instance, error) {
	return srvInst(m), nil
}
func (srvModule) Close(context.Context) error { return nil }

type srvInst struct {
	handler func(string, []byte) ([]byte, error)
}

func (srvInst) Meta(context.Context) ([]byte, error) {
	return json.Marshal(wasmplugins.Meta{
		ID: "echo", Name: "Echo", Version: "1.0.0", APIVersion: 1,
		Permissions: []string{wasmplugins.PermStorage},
		Routes: []wasmplugins.Route{
			{Method: "GET", Path: "/hello", Handler: "hello"}, {Method: "POST", Path: "/post", Handler: "post"},
			{Method: "GET", Path: "/boom", Handler: "boom"}, {Method: "GET", Path: "/", Handler: "root"},
		},
	})
}
func (srvInst) Init(context.Context, []byte) ([]byte, error) { return nil, nil }
func (i srvInst) Call(_ context.Context, name string, arg []byte) ([]byte, error) {
	return i.handler(name, arg)
}
func (srvInst) Close(context.Context) error { return nil }

func TestWasmPluginRoutesAndManagement(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "echo.wasm"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	eng := srvEngine{handler: func(name string, arg []byte) ([]byte, error) {
		switch name {
		case "hello":
			var r wasmplugins.Request
			_ = json.Unmarshal(arg, &r)
			return json.Marshal(map[string]any{"status": 201, "body": map[string]any{"q": r.Query["x"], "path": r.Path}})
		case "post":
			var r wasmplugins.Request
			_ = json.Unmarshal(arg, &r)
			return json.Marshal(map[string]any{"body": json.RawMessage(r.Body)})
		case "root":
			return []byte(`{"body":"root"}`), nil
		}
		return nil, errors.New("trap")
	}}
	mgr := wasmplugins.NewManager(context.Background(), wasmplugins.Options{Engine: eng, Dirs: []string{dir}})
	_ = mgr.Dirs()
	defer mgr.Close()
	mgr.Scan()

	// HTML-плагин с папкой api: файл из неё должен отдаваться, когда wasm-плагина с таким id нет
	htmlFS := fstest.MapFS{"widget/plugin.json": {Data: []byte(`{"id":"widget","name":"W","entry":"index.html"}`)}, "widget/index.html": {Data: []byte("<p>w</p>")}, "widget/api/data.json": {Data: []byte(`{"static":true}`)}}
	reg := plugins.New(htmlFS, fstest.MapFS{})
	s := &Server{d: Deps{Wasm: mgr, Plugins: reg, Running: config.Config{Host: "127.0.0.1", Port: 8123}}}
	s.app = fiber.New()
	s.wasmRoutes()
	s.wasmPluginRoutes()
	s.app.Get("/plugins/:id/*", s.pluginFile)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go s.app.Listener(ln, fiber.ListenConfig{DisableStartupMessage: true})
	defer s.app.Shutdown()
	base := "http://" + ln.Addr().String()
	call := func(method, path, body string, hdr map[string]string) (int, string, http.Header) {
		req, _ := http.NewRequest(method, base+path, strings.NewReader(body))
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b), resp.Header
	}
	guard := map[string]string{"X-Overlay-Composer": "1"}

	// маршруты плагина
	code, body, h := call("GET", "/plugins/echo/api/hello?x=42", "", nil)
	if code != 201 || body != `{"path":"/hello","q":"42"}` || !strings.HasPrefix(h.Get("Content-Type"), "application/json") || h.Get("Cache-Control") != "no-store" {
		t.Fatalf("hello: %d %s %v", code, body, h)
	}
	if code, body, _ = call("POST", "/plugins/echo/api/post", `{"a":[1,2]}`, nil); code != 200 || body != `{"a":[1,2]}` {
		t.Fatalf("post: %d %s", code, body)
	}
	if code, body, _ = call("GET", "/plugins/echo/api", "", nil); code != 200 || body != `"root"` {
		t.Fatalf("root route: %d %s", code, body)
	}
	// sad path
	if code, _, _ = call("POST", "/plugins/echo/api/post", `<html>`, nil); code != 400 {
		t.Fatalf("non-JSON body must be 400, got %d", code)
	}
	if code, _, _ = call("GET", "/plugins/echo/api/nothing", "", nil); code != 404 {
		t.Fatalf("unknown route: %d", code)
	}
	if code, _, _ = call("POST", "/plugins/echo/api/hello", "", nil); code != 404 {
		t.Fatalf("wrong method: %d", code)
	}
	if code, _, _ = call("POST", "/plugins/ghost/api/x", `{}`, nil); code != 404 {
		t.Fatalf("unknown plugin: %d", code)
	}
	code, body, _ = call("GET", "/plugins/echo/api/boom", "", nil)
	if code != 500 || strings.Contains(body, "trap") {
		t.Fatalf("a crash must be a generic 500 without internals: %d %s", code, body)
	}
	if code, body, _ = call("GET", "/plugins/echo/api/hello?x=again", "", nil); code != 201 {
		t.Fatalf("plugin must be restarted after the crash: %d %s", code, body)
	}
	// файл HTML-плагина из папки api не перехвачен
	if code, body, _ = call("GET", "/plugins/widget/api/data.json", "", nil); code != 200 || !strings.Contains(body, "static") {
		t.Fatalf("html widget file under api/: %d %s", code, body)
	}

	// управление
	code, body, _ = call("GET", "/api/wasmplugins", "", nil)
	if code != 200 || !strings.Contains(body, `"id":"echo"`) || !strings.Contains(body, `"state":"running"`) || !strings.Contains(body, `"dirs":[`) {
		t.Fatalf("list: %d %s", code, body)
	}
	if code, _, _ = call("PUT", "/api/wasmplugins/echo/grants", `{"granted":["storage"]}`, nil); code != 400 {
		t.Fatalf("no guard header: %d", code)
	}
	code, body, _ = call("PUT", "/api/wasmplugins/echo/grants", `{"granted":["storage"]}`, guard)
	if code != 200 || !strings.Contains(body, `"granted":["storage"]`) {
		t.Fatalf("grants: %d %s", code, body)
	}
	if code, _, _ = call("PUT", "/api/wasmplugins/echo/grants", `{"granted":["net.http"]}`, guard); code != 400 {
		t.Fatalf("permission that was not requested: %d", code)
	}
	if code, _, _ = call("PUT", "/api/wasmplugins/ghost/grants", `{"granted":[]}`, guard); code != 404 {
		t.Fatalf("grants for unknown plugin: %d", code)
	}
	if code, _, _ = call("PUT", "/api/wasmplugins/echo/config", `[1]`, guard); code != 400 {
		t.Fatalf("config must be an object: %d", code)
	}
	if code, body, _ = call("PUT", "/api/wasmplugins/echo/config", `{"k":"v"}`, guard); code != 200 || !strings.Contains(body, `"k":"v"`) {
		t.Fatalf("config: %d %s", code, body)
	}
	if code, _, _ = call("POST", "/api/wasmplugins/echo/restart", "", guard); code != 200 {
		t.Fatalf("restart: %d", code)
	}
	if code, _, _ = call("POST", "/api/wasmplugins/ghost/restart", "", guard); code != 404 {
		t.Fatalf("restart unknown: %d", code)
	}
	// запрос «через прокси» (есть X-Forwarded-For) — не локальный: управлять плагинами нельзя
	if code, _, _ = call("GET", "/api/wasmplugins", "", map[string]string{"X-Forwarded-For": "1.2.3.4"}); code != 403 {
		t.Fatalf("remote management must be forbidden: %d", code)
	}
}

func TestWasmDisabled(t *testing.T) {
	s := &Server{d: Deps{Plugins: plugins.New(fstest.MapFS{}, fstest.MapFS{})}}
	s.app = fiber.New()
	s.wasmRoutes()
	s.wasmPluginRoutes()
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	go s.app.Listener(ln, fiber.ListenConfig{DisableStartupMessage: true})
	defer s.app.Shutdown()
	resp, err := http.Get("http://" + ln.Addr().String() + "/api/wasmplugins")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 501 {
		t.Fatalf("without the manager: %d", resp.StatusCode)
	}
	req, _ := http.NewRequest("POST", "http://"+ln.Addr().String()+"/plugins/x/api/y", strings.NewReader("{}"))
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("plugin route without the manager: %d", resp.StatusCode)
	}
	s.SyncWasm() // не должен паниковать без менеджера и моста
}

// uiEngine — плагин с виджетом: его статика берётся из секции oc.assets реального wasm-файла (собранного PackWasm).
type uiEngine struct{}

func (uiEngine) Compile(context.Context, []byte) (wasmplugins.Module, error) { return uiModule{}, nil }
func (uiEngine) Close(context.Context) error                                 { return nil }

type uiModule struct{}

func (uiModule) New(context.Context, wasmplugins.HostCall, io.Writer) (wasmplugins.Instance, error) {
	return uiInst{}, nil
}
func (uiModule) Close(context.Context) error { return nil }

type uiInst struct{}

func (uiInst) Meta(context.Context) ([]byte, error) {
	return json.Marshal(wasmplugins.Meta{
		ID: "ui", Name: "UI", Version: "1.0.0", APIVersion: 1,
		Routes:  []wasmplugins.Route{{Method: "GET", Path: "/secret", Handler: "secret"}},
		Widgets: []wasmplugins.Widget{{ID: "main", Name: "Main", Entry: "index.html"}},
	})
}
func (uiInst) Init(context.Context, []byte) ([]byte, error) { return nil, nil }
func (uiInst) Call(context.Context, string, []byte) ([]byte, error) {
	return []byte(`{"body":"api"}`), nil
}
func (uiInst) Close(context.Context) error { return nil }

func TestWasmStaticAssets(t *testing.T) {
	src := t.TempDir()
	_ = os.WriteFile(filepath.Join(src, "index.html"), []byte("<p>widget</p>"), 0o644)
	_ = os.MkdirAll(filepath.Join(src, "js"), 0o755)
	_ = os.WriteFile(filepath.Join(src, "js", "app.js"), []byte("1"), 0o644)
	wasm, err := wasmassets.PackWasm([]byte("\x00asm\x01\x00\x00\x00"), src)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ui.wasm"), wasm, 0o644); err != nil {
		t.Fatal(err)
	}
	mgr := wasmplugins.NewManager(context.Background(), wasmplugins.Options{Engine: uiEngine{}, Dirs: []string{dir}})
	defer mgr.Close()
	mgr.Scan()

	htmlFS := fstest.MapFS{"widget/plugin.json": {Data: []byte(`{"id":"widget","name":"W","entry":"index.html"}`)}, "widget/index.html": {Data: []byte("<p>w</p>")}, "widget/ui/own.txt": {Data: []byte("static")}}
	s := &Server{d: Deps{Wasm: mgr, Plugins: plugins.New(htmlFS, fstest.MapFS{}), Running: config.Config{Host: "127.0.0.1", Port: 8123}}}
	s.app = fiber.New()
	s.wasmPluginRoutes()
	s.app.Get("/plugins/:id/*", s.pluginFile)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go s.app.Listener(ln, fiber.ListenConfig{DisableStartupMessage: true})
	defer s.app.Shutdown()
	get := func(path string, hdr map[string]string) (int, string, http.Header) {
		req, _ := http.NewRequest("GET", "http://"+ln.Addr().String()+path, nil)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b), resp.Header
	}

	code, body, h := get("/plugins/ui/ui/index.html", nil)
	if code != 200 || body != "<p>widget</p>" || !strings.HasPrefix(h.Get("Content-Type"), "text/html") {
		t.Fatalf("index: %d %q %v", code, body, h)
	}
	if !strings.Contains(h.Get("Content-Security-Policy"), "sandbox") || h.Get("Access-Control-Allow-Origin") != "*" || h.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("security headers: %v", h)
	}
	if code, body, h = get("/plugins/ui/ui/js/app.js", nil); code != 200 || body != "1" || !strings.HasPrefix(h.Get("Content-Type"), "text/javascript") {
		t.Fatalf("nested file: %d %q %v", code, body, h)
	}
	// условный запрос
	etag := h.Get("ETag")
	if etag == "" {
		t.Fatal("ETag expected")
	}
	if code, body, _ = get("/plugins/ui/ui/js/app.js", map[string]string{"If-None-Match": etag}); code != 304 || body != "" {
		t.Fatalf("conditional: %d %q", code, body)
	}
	// sad paths: нет файла, выход из архива, каталог, чужой плагин; статика не должна открывать маршруты плагина
	for _, p := range []string{"/plugins/ui/ui/missing.js", "/plugins/ui/ui/", "/plugins/ui/ui/js", "/plugins/ui/ui/%2e%2e/api/secret", "/plugins/ui/ui/..%2fapi%2fsecret", "/plugins/ui/ui/js/../../ui.wasm"} {
		if code, body, _ = get(p, nil); code == 200 {
			t.Fatalf("%s must not be served: %q", p, body)
		}
	}
	// HTML-плагин с папкой ui по-прежнему отдаёт свои файлы
	if code, body, _ = get("/plugins/widget/ui/own.txt", nil); code != 200 || body != "static" {
		t.Fatalf("html plugin ui folder: %d %q", code, body)
	}
	// ответ маршрута плагина остаётся JSON и без CSP-«песочницы» статики
	if code, _, h = get("/plugins/ui/api/secret", nil); code != 200 || !strings.HasPrefix(h.Get("Content-Type"), "application/json") {
		t.Fatalf("api: %d %v", code, h)
	}
}
