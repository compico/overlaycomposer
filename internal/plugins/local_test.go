package plugins

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func lf(kv ...string) []LocalFile {
	var out []LocalFile
	for i := 0; i < len(kv); i += 2 {
		out = append(out, LocalFile{Path: kv[i], Data: []byte(kv[i+1])})
	}
	return out
}

func newLocalReg(t *testing.T) (string, *Registry) {
	t.Helper()
	dir := t.TempDir()
	return dir, New(fstest.MapFS{}, os.DirFS(dir))
}

func TestInstallLocalMakesWorkingPlugin(t *testing.T) {
	dir, reg := newLocalReg(t)
	m, err := InstallLocal(dir, reg, "My Alert", lf("alert.html", "<h1>hi</h1>", "css/a.css", "b{}"))
	if err != nil {
		t.Fatal(err)
	}
	if m.ID != "local-my-alert" || m.Entry != "alert.html" || len(m.Permissions) != 0 {
		t.Fatalf("манифест: %+v", m)
	}
	reg.Scan()
	p, ok := reg.Get(m.ID)
	if !ok {
		t.Fatal("плагин не виден после Scan")
	}
	f, name, err := p.Open("")
	if err != nil || name != "alert.html" {
		t.Fatalf("entry: %v %q", err, name)
	}
	f.Close()
	if f2, _, err := p.Open("css/a.css"); err != nil {
		t.Fatalf("подпапка: %v", err)
	} else {
		f2.Close()
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".install-*")); len(left) != 0 {
		t.Fatalf("остались временные папки: %v", left)
	}
}

func TestInstallLocalPicksIndexAndUniqueIDs(t *testing.T) {
	dir, reg := newLocalReg(t)
	a, err := InstallLocal(dir, reg, "Same", lf("other.html", "x", "index.html", "y"))
	if err != nil || a.Entry != "index.html" {
		t.Fatalf("index.html должен быть точкой входа: %v %+v", err, a)
	}
	b, err := InstallLocal(dir, reg, "Same", lf("w.html", "z"))
	if err != nil || b.ID == a.ID || !strings.HasPrefix(b.ID, "local-same") {
		t.Fatalf("id должен быть свободным: %v %+v", err, b)
	}
}

func TestInstallLocalSadPaths(t *testing.T) {
	dir, reg := newLocalReg(t)
	big := make([]byte, MaxLocalBytes+1)
	cases := map[string]struct {
		name  string
		files []LocalFile
	}{
		"пустое имя":           {" ", lf("a.html", "x")},
		"нет файлов":           {"n", nil},
		"нет html":             {"n", lf("a.css", "x")},
		"несколько html":       {"n", lf("a.html", "x", "b.html", "y")},
		"выход из папки":       {"n", lf("a.html", "x", "../evil.html", "y")},
		"абсолютный путь":      {"n", lf("a.html", "x", "/etc/passwd", "y")},
		"скрытый файл":         {"n", lf("a.html", "x", ".env", "y")},
		"скрытая папка":        {"n", lf("a.html", "x", ".git/config", "y")},
		"свой plugin.json":     {"n", lf("a.html", "x", "plugin.json", "{}")},
		"дубликат":             {"n", lf("a.html", "x", "a.html", "y")},
		"слишком глубоко":      {"n", lf("a.html", "x", "a/b/c/d/e.js", "y")},
		"слишком большой":      {"n", []LocalFile{{Path: "a.html", Data: big}}},
		"обратный слэш наружу": {"n", lf("a.html", "x", `..\evil.html`, "y")},
	}
	for name, c := range cases {
		if _, err := InstallLocal(dir, reg, c.name, c.files); err == nil {
			t.Errorf("%s: ошибка ожидалась", name)
		}
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("при отказе на диске ничего не должно оставаться: %v", entries)
	}
}
