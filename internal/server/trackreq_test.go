package server

import (
	"encoding/json"
	"io"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"

	"github.com/compico/overlaycomposer/internal/store"
	"github.com/compico/overlaycomposer/internal/trackreq"
)

func trackReqServer(t *testing.T, withSvc bool) *Server {
	t.Helper()
	s := &Server{}
	if withSvc {
		st, err := store.Open(filepath.Join(t.TempDir(), "oc.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = st.Close() })
		svc, err := trackreq.New(trackreq.FromStore(st), nil, trackreq.NoPlayer{}, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(svc.Stop)
		s.d.TrackReq = svc
	}
	s.app = fiber.New()
	// без localOnly: app.Test — не loopback; сама защита localOnly проверяется отдельно ниже
	s.app.Get("/api/trackreq", s.trackReqStatus)
	s.app.Put("/api/trackreq/config", s.appGuard, s.trackReqSetConfig)
	s.app.Post("/api/trackreq/requests/:id/cancel", s.appGuard, s.trackReqCancel)
	return s
}

func call(t *testing.T, s *Server, method, path, body string, guard bool) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if guard {
		req.Header.Set("X-Overlay-Composer", "1")
	}
	resp, err := s.app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return resp.StatusCode, m
}

func TestTrackReqAPI(t *testing.T) {
	s := trackReqServer(t, true)

	code, m := call(t, s, "GET", "/api/trackreq", "", false)
	if code != 200 || m["requests"] == nil {
		t.Fatalf("статус: %d %v", code, m)
	}
	if code, _ := call(t, s, "PUT", "/api/trackreq/config", `{"enabled":true,"rewardId":"rw"}`, true); code != 200 {
		t.Fatalf("включение: %d", code)
	}
	_, m = call(t, s, "GET", "/api/trackreq", "", false)
	cfg := m["status"].(map[string]any)["config"].(map[string]any)
	if cfg["enabled"] != true || cfg["rewardId"] != "rw" {
		t.Fatalf("настройки не сохранились: %v", cfg)
	}

	// sad path
	if code, _ := call(t, s, "PUT", "/api/trackreq/config", `{"enabled":true,"rewardId":""}`, true); code != 400 {
		t.Fatalf("включение без награды: %d", code)
	}
	if code, _ := call(t, s, "PUT", "/api/trackreq/config", `{"enabled":false}`, false); code != 400 {
		t.Fatalf("без заголовка защиты: %d", code)
	}
	if code, _ := call(t, s, "PUT", "/api/trackreq/config", `not json`, true); code != 400 {
		t.Fatalf("битое тело: %d", code)
	}
	if code, _ := call(t, s, "POST", "/api/trackreq/requests/999/cancel", "", true); code != 404 {
		t.Fatalf("неизвестная заявка: %d", code)
	}
	if code, _ := call(t, s, "POST", "/api/trackreq/requests/abc/cancel", "", true); code != 400 {
		t.Fatalf("плохой id: %d", code)
	}
}

func TestTrackReqUnavailableAndLocalOnly(t *testing.T) {
	s := trackReqServer(t, false)
	if code, _ := call(t, s, "GET", "/api/trackreq", "", false); code != 503 {
		t.Fatalf("без сервиса: %d", code)
	}
	// настоящие маршруты закрыты не-локальным запросам (app.Test приходит не с loopback)
	s2 := &Server{app: fiber.New()}
	s2.trackReqRoutes()
	for _, p := range [][2]string{{"GET", "/api/trackreq"}, {"GET", "/api/trackreq/rewards"}, {"PUT", "/api/trackreq/config"}} {
		if code, _ := call(t, s2, p[0], p[1], "{}", true); code != 403 {
			t.Errorf("%s %s: %d, ожидался 403", p[0], p[1], code)
		}
	}
}
