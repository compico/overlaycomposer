package server

import (
	"encoding/json"
	"io"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"

	"github.com/compico/overlaycomposer/internal/hub"
	"github.com/compico/overlaycomposer/internal/model"
	"github.com/compico/overlaycomposer/internal/store"
)

// Сохранение layout из двух вкладок: устаревшая версия получает 409 и ничего не затирает.
func TestPutLayoutRevConflict(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "oc.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, _, err := st.SaveLayoutRev(model.Layout{ID: "a", Name: "A", Scenes: []string{}, Canvas: model.Canvas{Width: 1920, Height: 1080}, Objects: []model.Object{}}, nil); err != nil {
		t.Fatal(err)
	}
	h := hub.New(st, stubObs{}, func() string { return "" }, func() bool { return false })
	s := &Server{d: Deps{Store: st, Hub: h}}
	s.app = fiber.New()
	s.app.Put("/api/layouts/:id", s.putLayout)

	put := func(name string, rev *int64) (int, map[string]any) {
		b := map[string]any{"name": name, "scenes": []string{}, "canvas": map[string]int{"width": 1920, "height": 1080}, "objects": []any{}}
		if rev != nil {
			b["rev"] = *rev
		}
		raw, _ := json.Marshal(b)
		req := httptest.NewRequest("PUT", "/api/layouts/a", strings.NewReader(string(raw)))
		req.Header.Set("Content-Type", "application/json")
		resp, err := s.app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		out, _ := io.ReadAll(resp.Body)
		var m map[string]any
		_ = json.Unmarshal(out, &m)
		return resp.StatusCode, m
	}
	one := int64(1)

	code, m := put("Tab 1", &one)
	if code != 200 || m["rev"] != float64(2) {
		t.Fatalf("первое сохранение: %d %v", code, m)
	}
	// sad path: вторая вкладка всё ещё считает, что правит версию 1
	code, m = put("Tab 2", &one)
	if code != 409 || m["rev"] != float64(2) {
		t.Fatalf("конфликт: %d %v", code, m)
	}
	if l, _ := st.GetLayout("a"); l.Name != "Tab 1" {
		t.Fatalf("конфликтный запрос затёр данные: %q", l.Name)
	}
	// клиент без rev (скрипт) сохраняет как раньше
	if code, _ = put("Script", nil); code != 200 {
		t.Fatalf("без rev: %d", code)
	}
}
