package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"

	"github.com/compico/overlaycomposer/internal/bundle"
	"github.com/compico/overlaycomposer/internal/paths"
)

type fakeSection struct{ data map[string]string }

func (f *fakeSection) Name() string { return "fake" }
func (f *fakeSection) Export(context.Context) (json.RawMessage, error) {
	return json.Marshal(f.data)
}
func (f *fakeSection) Plan(_ context.Context, raw json.RawMessage) ([]bundle.Item, error) {
	var in map[string]string
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, err
	}
	out := []bundle.Item{}
	for id := range in {
		out = append(out, bundle.Item{ID: id, Status: bundle.StatusNew})
	}
	return out, nil
}
func (f *fakeSection) Apply(_ context.Context, raw json.RawMessage, pick map[string]string) ([]bundle.Outcome, error) {
	var in map[string]string
	_ = json.Unmarshal(raw, &in)
	out := []bundle.Outcome{}
	for id := range pick {
		f.data[id] = in[id]
		out = append(out, bundle.Outcome{ID: id, OK: true})
	}
	return out, nil
}

func settingsEnv(t *testing.T, data map[string]string) (*Server, string) {
	dir := t.TempDir()
	s := &Server{d: Deps{Paths: paths.Paths{DataDir: dir}}}
	svc := bundle.New("test", &fakeSection{data: data})
	svc.BeforeApply = s.backupSettings
	s.bundleSvc = svc
	s.bundleOnce.Do(func() {}) // сервис подставлен вручную
	s.app = fiber.New()
	s.app.Get("/export", s.settingsExport)
	s.app.Post("/plan", s.settingsPlan)
	s.app.Post("/apply", s.settingsApply)
	return s, dir
}

func call(t *testing.T, s *Server, method, path, body string) (int, string) {
	t.Helper()
	resp, err := appTest(s.app, httptest.NewRequest(method, path, strings.NewReader(body)))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestSettingsExportPlanApplyAndBackup(t *testing.T) {
	src, _ := settingsEnv(t, map[string]string{"a": "1"})
	code, exported := call(t, src, "GET", "/export", "")
	if code != 200 || !strings.Contains(exported, `"format":1`) {
		t.Fatalf("%d %s", code, exported)
	}

	dst, dir := settingsEnv(t, map[string]string{})
	code, plan := call(t, dst, "POST", "/plan", exported)
	if code != 200 || !strings.Contains(plan, `"id":"a"`) || !strings.Contains(plan, `"status":"new"`) {
		t.Fatalf("%d %s", code, plan)
	}
	req, _ := json.Marshal(map[string]any{"file": json.RawMessage(exported), "selection": map[string]map[string]string{"fake": {"a": "replace"}}})
	code, res := call(t, dst, "POST", "/apply", string(req))
	if code != 200 || !strings.Contains(res, `"ok":true`) {
		t.Fatalf("%d %s", code, res)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "backups", "oc-settings-*.json"))
	if len(files) != 1 {
		t.Fatalf("бэкап перед импортом: %v", files)
	}
	if b, _ := os.ReadFile(files[0]); !strings.Contains(string(b), `"fake"`) {
		t.Fatal("в бэкапе нет текущих настроек")
	}
}

func TestSettingsRejectsBadFiles(t *testing.T) {
	s, _ := settingsEnv(t, map[string]string{})
	for _, body := range []string{"{", `{"hello":1}`, `{"format":99,"sections":{},"checksums":{}}`} {
		if code, out := call(t, s, "POST", "/plan", body); code != 400 || !strings.Contains(out, "error") {
			t.Errorf("%q: %d %s", body, code, out)
		}
	}
	if code, _ := call(t, s, "POST", "/apply", `{"file":{"format":1,"sections":{},"checksums":{}},"selection":{"nope":{"a":"replace"}}}`); code != 400 {
		t.Fatalf("неизвестная секция: %d", code)
	}
	if code, _ := call(t, s, "GET", "/export?sections=nope", ""); code != 400 {
		t.Fatalf("export неизвестной секции: %d", code)
	}
}
