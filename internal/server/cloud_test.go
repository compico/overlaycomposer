package server

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/compico/overlaycomposer/internal/cloud"
)

func cloudEnv(t *testing.T) *Server {
	s, _ := settingsEnv(t, map[string]string{"a": "1"})
	s.app.Get("/cloud", s.cloudStatus)
	s.app.Put("/cloud", s.cloudConnect)
	s.app.Delete("/cloud", s.cloudDisconnect)
	s.app.Get("/cloud/files", s.cloudList)
	s.app.Post("/cloud/upload", s.cloudUpload)
	s.app.Post("/cloud/google/start", s.googleStart)
	s.app.Get("/google/callback", s.googleCallback)
	s.app.Get("/google/done", s.googleDone)
	s.app.Get("/cloud/files/:id", s.cloudDownload)
	s.app.Delete("/cloud/files/:id", s.cloudDelete)
	s.cloudLoaded = true // Store в тесте нет: сохранённой папки не ищем
	return s
}

func TestCloudRequiresConnection(t *testing.T) {
	s := cloudEnv(t)
	if code, out := call(t, s, "GET", "/cloud", ""); code != 200 || !strings.Contains(out, `"connected":false`) {
		t.Fatalf("%d %s", code, out)
	}
	for _, path := range []string{"/cloud/files", "/cloud/files/oc-settings-x.json"} {
		if code, _ := call(t, s, "GET", path, ""); code != 400 {
			t.Errorf("%s без подключения: %d", path, code)
		}
	}
	if code, _ := call(t, s, "POST", "/cloud/upload", ""); code != 400 {
		t.Fatalf("upload без подключения: %d", code)
	}
	if code, _ := call(t, s, "PUT", "/cloud", `{"folder":"relative"}`); code != 400 {
		t.Fatalf("относительная папка принята: %d", code)
	}
}

func TestCloudUploadListDownloadRoundTrip(t *testing.T) {
	s := cloudEnv(t)
	dir := filepath.Join(t.TempDir(), "sync")
	reqDir, _ := json.Marshal(map[string]string{"folder": dir})
	if code, out := call(t, s, "PUT", "/cloud", string(reqDir)); code != 200 || !strings.Contains(out, `"connected":true`) {
		t.Fatalf("connect: %d %s", code, out)
	}

	code, out := call(t, s, "POST", "/cloud/upload", `{"name":"oc-settings-test.json"}`)
	var up struct{ Fingerprint string }
	if code != 200 || json.Unmarshal([]byte(out), &up) != nil || up.Fingerprint == "" {
		t.Fatalf("upload: %d %s", code, out)
	}
	if code, out := call(t, s, "GET", "/cloud/files", ""); code != 200 || !strings.Contains(out, "oc-settings-test.json") {
		t.Fatalf("list: %d %s", code, out)
	}
	code, dl := call(t, s, "GET", "/cloud/files/oc-settings-test.json", "")
	var got struct{ ID, Text string }
	if code != 200 || json.Unmarshal([]byte(dl), &got) != nil || got.ID != "oc-settings-test.json" {
		t.Fatalf("download: %d %s", code, dl)
	}
	file := got.Text
	if !strings.Contains(file, `"format": 1`) || !strings.Contains(file, `"fake"`) {
		t.Fatalf("текст файла: %s", file)
	}
	// скачанный файл читается обычным импортом
	if code, plan := call(t, s, "POST", "/plan", file); code != 200 || !strings.Contains(plan, `"id":"a"`) {
		t.Fatalf("plan: %d %s", code, plan)
	}

	// чужую версию без отпечатка не перезаписываем
	if code, out := call(t, s, "POST", "/cloud/upload", `{"name":"oc-settings-test.json"}`); code != 409 || !strings.Contains(out, `"code":"conflict"`) {
		t.Fatalf("перезапись без expect: %d %s", code, out)
	}
	req, _ := json.Marshal(map[string]string{"name": "oc-settings-test.json", "expect": up.Fingerprint})
	if code, out := call(t, s, "POST", "/cloud/upload", string(req)); code != 200 {
		t.Fatalf("замена известной версии: %d %s", code, out)
	}

	if code, _ := call(t, s, "DELETE", "/cloud/files/oc-settings-test.json", ""); code != 200 {
		t.Fatalf("delete: %d", code)
	}
	if code, _ := call(t, s, "GET", "/cloud/files/oc-settings-test.json", ""); code != 404 {
		t.Fatalf("после delete: %d", code)
	}
	if code, out := call(t, s, "DELETE", "/cloud", ""); code != 200 || !strings.Contains(out, `"connected":false`) {
		t.Fatalf("disconnect: %d %s", code, out)
	}
}

func TestCloudRejectsBadNames(t *testing.T) {
	s := cloudEnv(t)
	reqDir, _ := json.Marshal(map[string]string{"folder": t.TempDir()})
	call(t, s, "PUT", "/cloud", string(reqDir))
	for _, name := range []string{"../x.json", "settings.json", "oc-settings-a/b.json"} {
		req, _ := json.Marshal(map[string]string{"name": name})
		if code, _ := call(t, s, "POST", "/cloud/upload", string(req)); code != 400 {
			t.Errorf("имя %q принято: %d", name, code)
		}
	}
}

func TestCloudFileName(t *testing.T) {
	at := time.Date(2026, 11, 21, 11, 30, 5, 0, time.UTC)
	for host, want := range map[string]string{
		"DESKTOP-1":     "oc-settings-DESKTOP-1-20261121-113005.json",
		"my pc/../évil": "oc-settings-my-pc-.-vil-20261121-113005.json",
		"":              "oc-settings-pc-20261121-113005.json",
	} {
		got := cloudFileName(host, at)
		if err := cloud.ValidName(got); err != nil {
			t.Errorf("%q → %q: %v", host, got, err)
		}
		if host == "DESKTOP-1" || host == "" {
			if got != want {
				t.Errorf("%q → %q, want %q", host, got, want)
			}
		}
	}
}

func TestGoogleDriveUnavailableWithoutClient(t *testing.T) {
	s := cloudEnv(t) // нет базы и ключа шифрования: Google Drive не создаётся
	if code, out := call(t, s, "GET", "/cloud", ""); code != 200 || !strings.Contains(out, `"google":{"available":false,"connected":false}`) {
		t.Fatalf("status: %d %s", code, out)
	}
	if code, out := call(t, s, "POST", "/cloud/google/start", ""); code != 400 || !strings.Contains(out, "not available") {
		t.Fatalf("start: %d %s", code, out)
	}
	if code, _ := call(t, s, "GET", "/google/callback?code=x&state=y", ""); code != 303 {
		t.Fatalf("callback без клиента должен перенаправлять: %d", code)
	}
	if code, out := call(t, s, "GET", "/google/done?status=failed", ""); code != 400 || strings.Contains(out, "code=") {
		t.Fatalf("done failed: %d %s", code, out)
	}
	if code, out := call(t, s, "GET", "/google/done?status=ok", ""); code != 200 || !strings.Contains(out, "Google Drive connected") {
		t.Fatalf("done ok: %d %s", code, out)
	}
	if code, out := call(t, s, "GET", "/google/done?status=<script>", ""); code != 400 || strings.Contains(out, "<script>") {
		t.Fatalf("done не должен отражать параметры: %d %s", code, out)
	}
}
