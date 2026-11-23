package server

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/compico/overlaycomposer/internal/bundle"
)

type memKV map[string]string

func (m memKV) Setting(k string) (string, bool, error) { v, ok := m[k]; return v, ok, nil }
func (m memKV) SetSetting(k, v string) error           { m[k] = v; return nil }

func snapEnv(t *testing.T, data map[string]string) (*Server, memKV, string) {
	s, dir := settingsEnv(t, data)
	kv := memKV{}
	s.kvImpl = kv
	s.app.Get("/backup", s.backupInfo)
	s.app.Put("/backup", s.backupPut)
	s.app.Post("/backup/snapshot", s.backupSnapshot)
	s.app.Get("/backup/files/:name", s.backupFile)
	s.app.Delete("/backup/files/:name", s.backupDelete)
	return s, kv, filepath.Join(dir, "backups")
}

func snapshots(t *testing.T, dir string) []bundle.Backup {
	list, err := bundle.ListBackups(dir)
	if err != nil {
		t.Fatal(err)
	}
	return list
}

func TestAutoSnapshotOnlyWhenChangedAndEnabled(t *testing.T) {
	data := map[string]string{"a": "1"}
	s, kv, dir := snapEnv(t, data)
	ctx := t.Context()

	s.AutoSnapshot(ctx)
	if n := len(snapshots(t, dir)); n != 1 {
		t.Fatalf("первое закрытие: %d снимков", n)
	}
	s.AutoSnapshot(ctx) // ничего не менялось
	if n := len(snapshots(t, dir)); n != 1 {
		t.Fatalf("без изменений снимок записан: %d", n)
	}
	data["b"] = "2"
	s.AutoSnapshot(ctx)
	list := snapshots(t, dir)
	if len(list) != 2 || list[0].Kind != bundle.KindAuto {
		t.Fatalf("после изменения: %+v", list)
	}

	kv[backupAutoKey] = "0" // выключено в настройках
	data["c"] = "3"
	s.AutoSnapshot(ctx)
	if n := len(snapshots(t, dir)); n != 2 {
		t.Fatalf("выключенный автоснимок записан: %d", n)
	}
}

func TestManualSnapshotCountsAsLastSnapshot(t *testing.T) {
	s, _, dir := snapEnv(t, map[string]string{"a": "1"})
	if code, _ := call(t, s, "POST", "/backup/snapshot", ""); code != 200 {
		t.Fatalf("snapshot: %d", code)
	}
	s.AutoSnapshot(t.Context()) // настройки те же, что в ручном снимке
	list := snapshots(t, dir)
	if len(list) != 1 || list[0].Kind != bundle.KindManual {
		t.Fatalf("%+v", list)
	}
}

func TestBackupLimitDefaultsAndPut(t *testing.T) {
	s, _, dir := snapEnv(t, map[string]string{"a": "1"})
	code, out := call(t, s, "GET", "/backup", "")
	if code != 200 || !strings.Contains(out, `"auto":true`) || !strings.Contains(out, `"max":30`) {
		t.Fatalf("по умолчанию: %d %s", code, out)
	}
	for _, bad := range []string{`{"max":0}`, `{"max":201}`, `{"max":"x"}`, `nope`} {
		if code, _ := call(t, s, "PUT", "/backup", bad); code != 400 {
			t.Errorf("%s принято: %d", bad, code)
		}
	}
	if code, out := call(t, s, "PUT", "/backup", `{"max":2,"auto":false}`); code != 200 || !strings.Contains(out, `"max":2`) || !strings.Contains(out, `"auto":false`) {
		t.Fatalf("put: %d %s", code, out)
	}
	for i := 0; i < 4; i++ {
		if _, err := s.writeSnapshot(t.Context(), bundle.KindManual); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond) // имя файла — по времени с миллисекундами
	}
	if n := len(snapshots(t, dir)); n != 2 {
		t.Fatalf("лимит 2, файлов %d", n)
	}
}

func TestBackupFileReadDeleteAndNames(t *testing.T) {
	s, _, dir := snapEnv(t, map[string]string{"a": "1"})
	call(t, s, "POST", "/backup/snapshot", "")
	list := snapshots(t, dir)
	if len(list) != 1 {
		t.Fatal("нет снимка")
	}
	code, out := call(t, s, "GET", "/backup/files/"+list[0].Name, "")
	var got struct{ Name, Text string }
	if code != 200 || json.Unmarshal([]byte(out), &got) != nil || !strings.Contains(got.Text, `"format": 1`) {
		t.Fatalf("read: %d %s", code, out)
	}
	// скачанный снимок читается обычным импортом
	if code, plan := call(t, s, "POST", "/plan", got.Text); code != 200 || !strings.Contains(plan, `"id":"a"`) {
		t.Fatalf("plan: %d %s", code, plan)
	}
	for _, bad := range []string{"notes.txt", "..%2Fx.json", "oc-settings-x.json"} {
		if code, _ := call(t, s, "GET", "/backup/files/"+bad, ""); code != 404 {
			t.Errorf("read %q: %d", bad, code)
		}
		if code, _ := call(t, s, "DELETE", "/backup/files/"+bad, ""); code != 404 {
			t.Errorf("delete %q: %d", bad, code)
		}
	}
	if code, _ := call(t, s, "DELETE", "/backup/files/"+list[0].Name, ""); code != 200 {
		t.Fatalf("delete: %d", code)
	}
	if n := len(snapshots(t, dir)); n != 0 {
		t.Fatalf("после delete: %d", n)
	}
}
