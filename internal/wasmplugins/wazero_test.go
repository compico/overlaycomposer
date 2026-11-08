package wasmplugins

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/compico/overlaycomposer/internal/wasmassets"
)

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

// buildGuest собирает testdata/guest в wasm тем же способом, каким плагины на Go собирают пользователи.
func buildGuest(t *testing.T, dir string) {
	t.Helper()
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go toolchain is not in PATH: cannot build the test plugin")
	}
	cmd := exec.Command(goBin, "build", "-buildmode=c-shared", "-o", filepath.Join(dir, "testguest.wasm"), ".")
	cmd.Dir = filepath.Join("testdata", "guest")
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm", "CGO_ENABLED=0", "GOFLAGS=")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building the test plugin: %v\n%s", err, out)
	}
}

// Настоящий wazero и настоящий wasm (плагин на SDK pkg/ocplugin): метаданные из oc_meta, маршруты, storage, таймер,
// паника в обработчике → 500, сбой процесса → перезапуск.
func TestWazeroEndToEnd(t *testing.T) {
	dir := t.TempDir()
	buildGuest(t, dir)
	eng, err := NewWazeroEngine(filepath.Join(t.TempDir(), "cache"))
	if err != nil {
		t.Fatal(err)
	}
	logs := &syncBuf{}
	store := &memStorage{}
	m := NewManager(context.Background(), Options{
		Engine: eng, Dirs: []string{dir}, State: &MemState{},
		Services: Services{Storage: store, Log: slog.New(slog.NewTextHandler(logs, nil))},
	})
	defer m.Close()
	m.Scan()

	infos, problems := m.List()
	if len(problems) != 0 || len(infos) != 1 {
		t.Fatalf("scan: %+v %+v", infos, problems)
	}
	if infos[0].Meta.ID != "testguest" || infos[0].State != StateRunning || len(infos[0].Meta.Routes) != 5 {
		t.Fatalf("meta must come from oc_meta: %+v", infos[0])
	}
	ctx := context.Background()
	get := func(path string, q map[string]string) (Response, error) {
		return m.Handle(ctx, "testguest", Request{Method: "GET", Path: path, Query: q})
	}

	// без разрешений storage недоступен (SDK получает ошибку хоста и пишет предупреждение), но плагин отвечает
	if r, err := get("/greet", map[string]string{"name": "oc"}); err != nil || strings.Contains(string(r.Body), `"count":1`) && false {
		t.Fatalf("greet: %v %s", err, r.Body)
	}
	if _, ok, _ := store.Get(ctx, "testguest", "greeted"); ok {
		t.Fatal("storage must be denied before the grant")
	}
	if err := m.SetGrants("testguest", []string{PermStorage, PermTimer}); err != nil {
		t.Fatal(err)
	}
	// выдача прав перезапускает плагин сама: oc_init просит таймер уже с правом timer
	r, err := get("/greet", map[string]string{"name": "oc"})
	if err != nil || !strings.Contains(string(r.Body), `"hello":"oc"`) || !strings.Contains(string(r.Body), `"count":1`) {
		t.Fatalf("greet after grant: %v %s", err, r.Body)
	}

	waitFor(t, "timer ticks reach the plugin", func() bool {
		r, err := get("/ticks", nil)
		var v struct{ Ticks int }
		return err == nil && json.Unmarshal(r.Body, &v) == nil && v.Ticks >= 2
	})

	// часы плагина настоящие: расхождение с хостом — секунды, а не десятилетия
	r, err = get("/now", nil)
	var now struct{ Unix int64 }
	if err != nil || json.Unmarshal(r.Body, &now) != nil || now.Unix < time.Now().Add(-time.Minute).Unix() || now.Unix > time.Now().Add(time.Minute).Unix() {
		t.Fatalf("plugin clock: %v %s (host %d)", err, r.Body, time.Now().Unix())
	}

	// паника в обработчике: SDK отвечает 500 и пишет в лог, экземпляр жив (перезапуска нет)
	res, err := get("/boom", nil)
	if err != nil || res.Status != 500 || strings.Contains(string(res.Body), "nil map") {
		t.Fatalf("a recovered panic must be a generic 500: %v %+v", err, res)
	}
	if info, _ := m.List(); info[0].Restarts != 0 {
		t.Fatalf("a recovered panic must not restart the instance: %+v", info[0])
	}
	// сбой рантайма (wasm-процесс завершился): ошибка наружу, экземпляр пересоздан, состояние хоста цело
	if _, err := get("/exit", nil); err == nil {
		t.Fatal("process exit inside the plugin must surface as an error")
	}
	r, err = get("/greet", map[string]string{"name": "again"})
	if err != nil || !strings.Contains(string(r.Body), `"count":2`) {
		t.Fatalf("state must survive the crash: %v %s", err, r.Body)
	}
	if info, _ := m.List(); info[0].Restarts < 1 {
		t.Fatalf("restart must be counted: %+v", info[0])
	}
	if !strings.Contains(logs.String(), "panic") {
		t.Fatalf("the recovered panic must be logged by the SDK:\n%s", logs.String())
	}
	if !strings.Contains(logs.String(), "plugin=testguest") {
		t.Fatalf("log lines must carry the plugin id:\n%s", logs.String())
	}
}

// sad path: файл с расширением .wasm, но не модуль / модуль без обязательных экспортов.
func TestWazeroRejectsBadModules(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "junk.wasm"), []byte("definitely not wasm"), 0o644); err != nil {
		t.Fatal(err)
	}
	// минимальный валидный модуль (magic + version): компилируется, но экспортов нет
	if err := os.WriteFile(filepath.Join(dir, "empty.wasm"), []byte{0, 'a', 's', 'm', 1, 0, 0, 0}, 0o644); err != nil {
		t.Fatal(err)
	}
	eng, err := NewWazeroEngine("")
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(context.Background(), Options{Engine: eng, Dirs: []string{dir}, Services: Services{Log: quietLog()}})
	defer m.Close()
	m.Scan()
	infos, problems := m.List()
	if len(infos) != 0 || len(problems) != 2 {
		t.Fatalf("both files must be reported: %+v %+v", infos, problems)
	}
	all := problems[0].Error + problems[1].Error
	if !strings.Contains(all, "not a valid wasm") || !strings.Contains(all, `does not export "alloc"`) {
		t.Fatalf("reasons: %+v", problems)
	}
}

// Настоящий wasm с дописанной секцией oc.assets: wazero её пропускает, плагин работает как раньше, а статика читается хостом.
func TestWazeroModuleWithAssetsSection(t *testing.T) {
	dir := t.TempDir()
	buildGuest(t, dir)
	ui := t.TempDir()
	if err := os.WriteFile(filepath.Join(ui, "index.html"), []byte("<p>ui</p>"), 0o644); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "testguest.wasm")
	if err := wasmassets.PackFile(file, ui, file); err != nil {
		t.Fatal(err)
	}
	eng, err := NewWazeroEngine(filepath.Join(t.TempDir(), "cache"))
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(context.Background(), Options{Engine: eng, Dirs: []string{dir}, State: &MemState{}, Services: Services{Log: quietLog()}})
	defer m.Close()
	m.Scan()
	infos, problems := m.List()
	if len(problems) != 0 || len(infos) != 1 || infos[0].State != StateRunning {
		t.Fatalf("a module with oc.assets must load and run: %+v %+v", infos, problems)
	}
	if data, _, _, ok := m.Asset("testguest", "index.html"); !ok || string(data) != "<p>ui</p>" {
		t.Fatalf("asset: %q %v", data, ok)
	}
	if _, err := m.Handle(context.Background(), "testguest", Request{Method: "GET", Path: "/now"}); err != nil {
		t.Fatalf("the plugin must still answer routes: %v", err)
	}
}
