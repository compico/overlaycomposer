package server

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/gofiber/fiber/v3"

	"github.com/compico/overlaycomposer/internal/store"
)

func dashEnv(t *testing.T) *Server {
	st, err := store.Open(filepath.Join(t.TempDir(), "oc.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s := &Server{d: Deps{Store: st}}
	s.app = fiber.New()
	s.dashboardRoutes()
	return s
}

func dashGet(t *testing.T, s *Server) (layout struct {
	Rev    int
	Panels []struct{ ID string }
}, saved bool) {
	t.Helper()
	code, body, _ := deckReq(t, s, "GET", "/api/dashboard/layout", "", false)
	if code != 200 {
		t.Fatalf("GET: %d %s", code, body)
	}
	var out struct {
		Layout  json.RawMessage
		Saved   bool
		Widgets []struct{ Plugin string }
		Grid    struct{ Cols, MaxRows, RowPx, MaxPanels int }
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	if out.Grid.Cols != 12 || out.Widgets == nil {
		t.Fatalf("сетка и список виджетов всегда в ответе: %s", body)
	}
	if err := json.Unmarshal(out.Layout, &layout); err != nil {
		t.Fatal(err)
	}
	return layout, out.Saved
}

const dashPanel = `{"id":"chat","plugin":"twitch-tools","widget":"chat","w":6,"h":6}`

func TestDashboardLayoutAPI(t *testing.T) {
	s := dashEnv(t)
	if l, saved := dashGet(t, s); saved || l.Rev != 0 || l.Panels == nil {
		t.Fatalf("по умолчанию: %+v saved=%v", l, saved)
	}
	// Сохранение: нужен заголовок приложения, ревизия должна совпасть, раскладка — пройти проверку.
	put := `{"rev":0,"panels":[` + dashPanel + `]}`
	if c, _, _ := deckReq(t, s, "PUT", "/api/dashboard/layout", put, false); c != 400 {
		t.Fatalf("без заголовка: %d", c)
	}
	if c, _, _ := deckReq(t, s, "PUT", "/api/dashboard/layout", `{"rev":0,"panels":[{"id":"a","plugin":"p","widget":"w","w":0,"h":1}]}`, true); c != 400 {
		t.Fatalf("плохой размер: %d", c)
	}
	if c, b, _ := deckReq(t, s, "PUT", "/api/dashboard/layout", put, true); c != 200 {
		t.Fatalf("сохранение: %d %s", c, b)
	}
	if l, saved := dashGet(t, s); !saved || l.Rev != 1 || len(l.Panels) != 1 || l.Panels[0].ID != "chat" {
		t.Fatalf("после сохранения: %+v saved=%v", l, saved)
	}
	if c, _, _ := deckReq(t, s, "PUT", "/api/dashboard/layout", put, true); c != 409 {
		t.Fatalf("старая ревизия должна давать 409, получили %d", c)
	}
	// Сброс: раскладка по умолчанию, ревизия не откатывается.
	if c, _, _ := deckReq(t, s, "DELETE", "/api/dashboard/layout", "", false); c != 400 {
		t.Fatalf("сброс без заголовка: %d", c)
	}
	if c, _, _ := deckReq(t, s, "DELETE", "/api/dashboard/layout", "", true); c != 200 {
		t.Fatalf("сброс: %d", c)
	}
	if l, saved := dashGet(t, s); saved || l.Rev != 2 {
		t.Fatalf("после сброса ревизия растёт: %+v saved=%v", l, saved)
	}
}
