package market

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/compico/overlaycomposer/internal/plugins"
)

type file struct {
	name string
	body string
	mode os.FileMode // 0 — обычный файл
}

func makeZip(t *testing.T, files ...file) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range files {
		h := &zip.FileHeader{Name: f.name, Method: zip.Deflate}
		if f.mode != 0 {
			h.SetMode(f.mode)
		}
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(f.body))
	}
	zw.Close()
	return buf.Bytes()
}

func manifest(id, version string) string {
	return fmt.Sprintf(`{"id":%q,"name":"Plugin %s","version":%q,"permissions":["chat.read"]}`, id, id, version)
}

func plug(id, version string) []file {
	return []file{
		{name: "repo-abc/plugins/" + id + "/plugin.json", body: manifest(id, version)},
		{name: "repo-abc/plugins/" + id + "/index.html", body: "<html>" + id + version + "</html>"},
		{name: "repo-abc/plugins/" + id + "/lib/a.js", body: "1"},
	}
}

// env — магазин с подменённым источником архивов: repos["owner/repo"] = zip.
type env struct {
	m     *Market
	dir   string
	data  string
	repos map[string][]byte
	calls atomic.Int32
	mu    sync.Mutex // repos читают параллельные загрузки каталога
}

func newEnv(t *testing.T, builtin ...string) *env {
	t.Helper()
	root := t.TempDir()
	e := &env{dir: filepath.Join(root, "plugins"), data: filepath.Join(root, "market.json"), repos: map[string][]byte{}}
	if err := os.MkdirAll(e.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	reg := plugins.New(nil, os.DirFS(e.dir))
	m, err := New(Config{
		File: e.data, Dir: e.dir, Reg: reg,
		Fetch: func(_ context.Context, r Repo) ([]byte, error) {
			e.calls.Add(1)
			e.mu.Lock()
			b, ok := e.repos[strings.ToLower(r.Owner+"/"+r.Name)]
			e.mu.Unlock()
			if !ok {
				return nil, errors.New("repository or branch not found (is it public?)")
			}
			return b, nil
		},
		Builtin: func(id string) bool {
			for _, b := range builtin {
				if b == id {
					return true
				}
			}
			return false
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	e.m = m
	return e
}

func TestParseRepo(t *testing.T) {
	ok := map[string]string{
		"compico/oc-plugins":                             "compico/oc-plugins",
		" compico/oc-plugins@v1.2 ":                      "compico/oc-plugins@v1.2",
		"https://github.com/compico/oc-plugins":          "compico/oc-plugins",
		"https://github.com/compico/oc-plugins.git":      "compico/oc-plugins",
		"github.com/a/b/tree/feature/x":                  "a/b@feature/x",
		"https://github.com/compico/oc-plugins/":         "compico/oc-plugins",
		"a/b.c_d-e@0123456789abcdef0123456789abcdef0123": "a/b.c_d-e@0123456789abcdef0123456789abcdef0123",
	}
	for in, want := range ok {
		r, err := ParseRepo(in)
		if err != nil || r.String() != want {
			t.Errorf("ParseRepo(%q) = %v, %v; want %s", in, r, err, want)
		}
	}
	for _, in := range []string{"", "a", "a/b/c", "-a/b", "a/..", "a/b@", "a/b@../x", "a/b@/x", "a/b@x/", "a b/c", "https://evil.com/a/b", "a/b?x=1", "a/b#x", "a/b@x y", "http://github.com.evil.io/a/b"} {
		if _, err := ParseRepo(in); err == nil {
			t.Errorf("ParseRepo(%q) must fail", in)
		}
	}
}

func TestVersionCompare(t *testing.T) {
	for _, c := range []struct {
		avail, have string
		want        bool
	}{
		{"1.1.0", "1.0.9", true}, {"1.0.0", "1.0.0", false}, {"1.0", "1.0.0", false}, {"0.9", "1.0", false},
		{"v2", "1.9", true}, {"", "1.0", false}, {"2.0", "", true}, {"abc", "1.0", true}, {"1.0", "1.0-beta", true},
	} {
		if got := newer(c.avail, c.have); got != c.want {
			t.Errorf("newer(%q,%q)=%v want %v", c.avail, c.have, got, c.want)
		}
	}
}

func TestFirstRunSeedsDefaultSourceOnce(t *testing.T) {
	e := newEnv(t)
	if got := e.m.Sources(); len(got) != 1 || got[0] != DefaultSource {
		t.Fatalf("sources: %v", got)
	}
	if err := e.m.RemoveSource(DefaultSource); err != nil {
		t.Fatal(err)
	}
	// Перезапуск: файл есть — официальный источник заново не появляется.
	m2, err := New(Config{File: e.data, Dir: e.dir})
	if err != nil {
		t.Fatal(err)
	}
	if got := m2.Sources(); len(got) != 0 {
		t.Fatalf("after restart: %v", got)
	}
}

func TestAddSourceAndCatalog(t *testing.T) {
	e := newEnv(t)
	e.repos["bob/tools"] = makeZip(t, append(plug("alpha", "1.0.0"), plug("beta", "0.1")...)...)
	if _, err := e.m.AddSource(context.Background(), "bob/missing"); err == nil {
		t.Fatal("unknown repo must fail")
	}
	e.repos["bob/empty"] = makeZip(t, file{name: "repo-abc/README.md", body: "x"})
	if _, err := e.m.AddSource(context.Background(), "bob/empty"); err == nil || !strings.Contains(err.Error(), "no plugins") {
		t.Fatalf("empty repo: %v", err)
	}
	key, err := e.m.AddSource(context.Background(), "https://github.com/Bob/tools")
	if err != nil || key != "Bob/tools" {
		t.Fatalf("add: %q %v", key, err)
	}
	if k2, _ := e.m.AddSource(context.Background(), "bob/tools"); k2 != "Bob/tools" || len(e.m.Sources()) != 2 { // регистр не плодит дубли
		t.Fatalf("dup: %q %v", k2, e.m.Sources())
	}
	cat := e.m.Catalog(context.Background(), true)
	if len(cat) != 2 {
		t.Fatalf("catalog: %+v", cat)
	}
	if cat[0].Repo != DefaultSource || cat[0].Error == "" { // официальный в тесте недоступен — ошибка одного источника не ломает остальные
		t.Fatalf("default source: %+v", cat[0])
	}
	got := cat[1].Plugins
	if len(got) != 2 || got[0].ID != "alpha" || got[1].ID != "beta" || got[0].Installed || got[0].Permissions[0] != "chat.read" {
		t.Fatalf("plugins: %+v", got)
	}
}

func TestCatalogCache(t *testing.T) {
	e := newEnv(t)
	e.repos["bob/tools"] = makeZip(t, plug("alpha", "1")...)
	e.m.AddSource(context.Background(), "bob/tools")
	n := e.calls.Load()
	e.m.Catalog(context.Background(), false)
	e.m.Catalog(context.Background(), false)
	if d := e.calls.Load() - n; d != 2 { // официальный источник недоступен, ошибки не кэшируются (2 запроса); bob/tools — из кэша
		t.Fatalf("fetches: %d", d)
	}
	n = e.calls.Load()
	e.m.Catalog(context.Background(), true)
	if e.calls.Load()-n != 2 {
		t.Fatalf("refresh must refetch, got %d", e.calls.Load()-n)
	}
}

func TestInstallUpdateUninstall(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.repos["bob/tools"] = makeZip(t, plug("alpha", "1.0.0")...)
	e.m.AddSource(ctx, "bob/tools")

	en, err := e.m.Install(ctx, "bob/tools", "alpha")
	if err != nil || !en.Installed || en.InstalledVersion != "1.0.0" || en.UpdateAvailable {
		t.Fatalf("install: %+v %v", en, err)
	}
	b, err := os.ReadFile(filepath.Join(e.dir, "alpha", "lib", "a.js"))
	if err != nil || string(b) != "1" {
		t.Fatalf("file not extracted: %v", err)
	}
	if _, ok := e.m.reg.Get("alpha"); !ok {
		t.Fatal("registry must see the plugin without restart")
	}
	entries, _ := os.ReadDir(e.dir)
	if len(entries) != 1 {
		t.Fatalf("temp dirs left behind: %v", entries)
	}

	// В репозитории вышла новая версия: файл удалён/добавлен, обновление заменяет папку целиком.
	e.repos["bob/tools"] = makeZip(t,
		file{name: "repo-abc/plugins/alpha/plugin.json", body: manifest("alpha", "1.1.0")},
		file{name: "repo-abc/plugins/alpha/index.html", body: "<html>new</html>"})
	cat := e.m.Catalog(ctx, true)
	var ent Entry
	for _, s := range cat {
		for _, p := range s.Plugins {
			ent = p
		}
	}
	if !ent.Installed || !ent.UpdateAvailable || ent.Version != "1.1.0" || ent.InstalledVersion != "1.0.0" {
		t.Fatalf("update flags: %+v", ent)
	}
	if _, err := e.m.Install(ctx, "bob/tools", "alpha"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(e.dir, "alpha", "lib")); !os.IsNotExist(err) {
		t.Fatal("old files must be gone after update")
	}
	if rec := e.m.Installed()["alpha"]; rec.Version != "1.1.0" || rec.Repo != "bob/tools" {
		t.Fatalf("record: %+v", rec)
	}

	// Перезапуск помнит установленное.
	m2, _ := New(Config{File: e.data, Dir: e.dir})
	if _, ok := m2.Installed()["alpha"]; !ok {
		t.Fatal("installed list must persist")
	}

	if err := e.m.Uninstall("alpha"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(e.dir, "alpha")); !os.IsNotExist(err) {
		t.Fatal("folder must be removed")
	}
	if _, ok := e.m.reg.Get("alpha"); ok {
		t.Fatal("registry must forget the plugin")
	}
	if err := e.m.Uninstall("alpha"); !errors.Is(err, ErrNotOurs) {
		t.Fatalf("second uninstall: %v", err)
	}
}

func TestInstallRefusals(t *testing.T) {
	e := newEnv(t, "clock")
	ctx := context.Background()
	e.repos["bob/tools"] = makeZip(t, append(append(plug("alpha", "1"), plug("clock", "1")...), plug("manual", "1")...)...)
	e.repos["eve/other"] = makeZip(t, plug("alpha", "9")...)
	e.m.AddSource(ctx, "bob/tools")
	e.m.AddSource(ctx, "eve/other")

	if _, err := e.m.Install(ctx, "bob/tools", "clock"); !errors.Is(err, ErrConflict) {
		t.Fatalf("builtin: %v", err)
	}
	// Папка, положенная вручную (или чужая), не перезаписывается и не удаляется магазином.
	os.MkdirAll(filepath.Join(e.dir, "manual"), 0o755)
	os.WriteFile(filepath.Join(e.dir, "manual", "mine.txt"), []byte("mine"), 0o644)
	if _, err := e.m.Install(ctx, "bob/tools", "manual"); !errors.Is(err, ErrConflict) {
		t.Fatalf("manual: %v", err)
	}
	if err := e.m.Uninstall("manual"); !errors.Is(err, ErrNotOurs) {
		t.Fatalf("uninstall manual: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(e.dir, "manual", "mine.txt")); string(b) != "mine" {
		t.Fatal("manual plugin touched")
	}
	// Плагин с тем же id из другого репозитория не подменяет уже установленный.
	if _, err := e.m.Install(ctx, "bob/tools", "alpha"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Install(ctx, "eve/other", "alpha"); !errors.Is(err, ErrConflict) {
		t.Fatalf("other repo: %v", err)
	}
	// В каталоге это видно заранее.
	for _, s := range e.m.Catalog(ctx, false) {
		for _, p := range s.Plugins {
			switch {
			case s.Repo == "eve/other" && p.ID == "alpha" && p.Blocked != "foreign",
				p.ID == "clock" && p.Blocked != "builtin",
				p.ID == "manual" && p.Blocked != "foreign":
				t.Errorf("blocked flag: %s %+v", s.Repo, p)
			}
		}
	}
	if _, err := e.m.Install(ctx, "bob/tools", "nope"); !errors.Is(err, ErrNoPlugin) {
		t.Fatalf("unknown plugin: %v", err)
	}
	if _, err := e.m.Install(ctx, "zed/unlisted", "alpha"); !errors.Is(err, ErrNoSource) {
		t.Fatalf("unlisted source must not be installable: %v", err)
	}
	if _, err := e.m.Install(ctx, "bob/tools", "../x"); err == nil {
		t.Fatal("bad id")
	}
}

func TestRemovingSourceKeepsInstalledPlugin(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.repos["bob/tools"] = makeZip(t, plug("alpha", "1")...)
	e.m.AddSource(ctx, "bob/tools")
	e.m.Install(ctx, "bob/tools", "alpha")
	if err := e.m.RemoveSource("bob/tools"); err != nil {
		t.Fatal(err)
	}
	if err := e.m.RemoveSource("bob/tools"); !errors.Is(err, ErrNoSource) {
		t.Fatalf("remove twice: %v", err)
	}
	if _, ok := e.m.reg.Get("alpha"); !ok {
		t.Fatal("plugin must stay installed")
	}
	if _, err := e.m.Install(ctx, "bob/tools", "alpha"); !errors.Is(err, ErrNoSource) {
		t.Fatalf("update from removed source: %v", err)
	}
	if err := e.m.Uninstall("alpha"); err != nil { // удалить можно и после удаления источника
		t.Fatal(err)
	}
}

func TestMaliciousArchive(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.repos["eve/bad"] = makeZip(t, append(plug("alpha", "1"),
		file{name: "repo-abc/plugins/alpha/../../../evil.txt", body: "x"},
		file{name: "repo-abc/plugins/alpha/../beta/plugin.json", body: manifest("beta", "1")},
		file{name: "/abs/plugins/alpha/abs.txt", body: "x"},
		file{name: "repo-abc/plugins/alpha/link", body: "/etc/passwd", mode: os.ModeSymlink | 0o777},
		file{name: "repo-abc/plugins/wrongname/plugin.json", body: manifest("other", "1")},
		file{name: "repo-abc/plugins/noentry/plugin.json", body: manifest("noentry", "1")},
		file{name: "repo-abc/plugins/broken/plugin.json", body: "{not json"},
		file{name: "other/plugins/alpha/x.txt", body: "x"}, // другой корень архива — это ещё один «репозиторий», берём как есть
	)...)
	if _, err := e.m.AddSource(ctx, "eve/bad"); err != nil {
		t.Fatal(err)
	}
	list := e.m.Catalog(ctx, true)[1].Plugins
	if len(list) != 1 || list[0].ID != "alpha" {
		t.Fatalf("only the valid plugin may be listed: %+v", list)
	}
	if _, err := e.m.Install(ctx, "eve/bad", "alpha"); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"link", "abs.txt"} {
		if _, err := os.Lstat(filepath.Join(e.dir, "alpha", p)); err == nil {
			t.Errorf("%s must not be extracted", p)
		}
	}
	root := filepath.Dir(e.dir)
	for _, p := range []string{filepath.Join(root, "evil.txt"), filepath.Join(e.dir, "evil.txt"), filepath.Join(e.dir, "beta")} {
		if _, err := os.Lstat(p); err == nil {
			t.Errorf("%s escaped the plugin folder", p)
		}
	}
}

func TestLimits(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	big := strings.Repeat("a", maxFile+1)
	e.repos["eve/big"] = makeZip(t, append(plug("alpha", "1"), file{name: "repo-abc/plugins/alpha/big.bin", body: big})...)
	e.m.st.Sources = append(e.m.st.Sources, "eve/big") // мимо AddSource: он только проверяет, что каталог читается
	if _, err := e.m.Install(ctx, "eve/big", "alpha"); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("big file: %v", err)
	}
	entries, _ := os.ReadDir(e.dir)
	if len(entries) != 0 {
		t.Fatalf("failed install must leave nothing behind: %v", entries)
	}
	if _, ok := e.m.Installed()["alpha"]; ok {
		t.Fatal("failed install must not be recorded")
	}
	e.repos["eve/junk"] = []byte("not a zip")
	e.m.st.Sources = append(e.m.st.Sources, "eve/junk")
	if _, err := e.m.Install(ctx, "eve/junk", "alpha"); err == nil {
		t.Fatal("garbage archive")
	}
}

func TestFailedUpdateKeepsOldVersion(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.repos["bob/tools"] = makeZip(t, plug("alpha", "1.0.0")...)
	e.m.AddSource(ctx, "bob/tools")
	e.m.Install(ctx, "bob/tools", "alpha")
	e.repos["bob/tools"] = makeZip(t, append(plug("alpha", "2.0.0"), file{name: "repo-abc/plugins/alpha/big.bin", body: strings.Repeat("a", maxFile+1)})...)
	if _, err := e.m.Install(ctx, "bob/tools", "alpha"); err == nil {
		t.Fatal("update must fail")
	}
	b, _ := os.ReadFile(filepath.Join(e.dir, "alpha", "index.html"))
	if string(b) != "<html>alpha1.0.0</html>" || e.m.Installed()["alpha"].Version != "1.0.0" {
		t.Fatalf("old version must survive: %q", b)
	}
}
