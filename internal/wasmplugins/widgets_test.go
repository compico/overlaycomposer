package wasmplugins

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"

	"github.com/compico/overlaycomposer/internal/wasmassets"
)

var wasmHeader = []byte("\x00asm\x01\x00\x00\x00")

func makeZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for n, c := range files {
		w, err := zw.Create(n)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(c))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestWidgetsValidate(t *testing.T) {
	ok := func(ws ...Widget) error { m := baseMeta("x"); m.Widgets = ws; return m.Validate() }
	w := Widget{ID: "queue", Name: "Queue", Entry: "index.html"}
	if err := ok(w); err != nil {
		t.Fatal(err)
	}
	m := baseMeta("x")
	m.Widgets = []Widget{w}
	_ = m.Validate()
	if m.Widgets[0].Width != 400 || m.Widgets[0].Height != 300 {
		t.Fatalf("defaults: %+v", m.Widgets[0])
	}
	bad := map[string][]Widget{
		"id":       {{ID: "Bad Id", Name: "N", Entry: "i.html"}},
		"dup":      {w, w},
		"name":     {{ID: "a", Name: " ", Entry: "i.html"}},
		"entry":    {{ID: "a", Name: "N", Entry: "../i.html"}},
		"not html": {{ID: "a", Name: "N", Entry: "app.js"}},
		"size":     {{ID: "a", Name: "N", Entry: "i.html", Width: 9000}},
		"negative": {{ID: "a", Name: "N", Entry: "i.html", Height: -1}},
	}
	for n, ws := range bad {
		if err := ok(ws...); err == nil {
			t.Errorf("%s: want error", n)
		}
	}
	many := make([]Widget, maxWidgets+1)
	for i := range many {
		many[i] = Widget{ID: "w" + strings.Repeat("a", i), Name: "N", Entry: "i.html"}
	}
	if err := ok(many...); err == nil {
		t.Error("too many")
	}
}

func TestManagerLoadsAssetsAndChecksWidgetEntry(t *testing.T) {
	dir := t.TempDir()
	zipOK := makeZip(t, map[string]string{"index.html": "<p>ui", "app.js": "1"})
	withAssets, _ := wasmassets.AppendSection(wasmHeader, wasmassets.Section, zipOK)
	noEntry, _ := wasmassets.AppendSection(wasmHeader, wasmassets.Section, makeZip(t, map[string]string{"other.html": "x"}))
	brokenZip, _ := wasmassets.AppendSection(wasmHeader, wasmassets.Section, []byte("not a zip"))

	meta := func(id string, ws ...Widget) Meta { m := baseMeta(id); m.Widgets = ws; return m }
	w := Widget{ID: "main", Name: "Main", Entry: "index.html"}
	eng := &fakeEngine{mods: map[string]*fakeModule{
		string(withAssets): {meta: meta("good", w), handler: nopHandler},
		string(noEntry):    {meta: meta("noentry", w), handler: nopHandler},
		string(brokenZip):  {meta: meta("broken"), handler: nopHandler},
	}}
	writeWasm(t, dir, "good.wasm", string(withAssets))
	writeWasm(t, dir, "noentry.wasm", string(noEntry))
	writeWasm(t, dir, "broken.wasm", string(brokenZip))
	m := newTestManager(t, eng, &MemState{}, dir)
	m.Scan()

	infos, problems := m.List()
	if len(infos) != 1 || infos[0].Meta.ID != "good" || len(infos[0].Meta.Widgets) != 1 {
		t.Fatalf("only the plugin with a valid UI loads: %+v", infos)
	}
	if len(problems) != 2 {
		t.Fatalf("problems: %+v", problems)
	}
	for _, p := range problems {
		if !strings.Contains(p.Error, "entry") && !strings.Contains(p.Error, "zip") {
			t.Errorf("unhelpful problem: %+v", p)
		}
	}
	data, mime, sha, ok := m.Asset("good", "index.html")
	if !ok || string(data) != "<p>ui" || !strings.HasPrefix(mime, "text/html") || len(sha) != 64 {
		t.Fatalf("asset: %q %q %q %v", data, mime, sha, ok)
	}
	if _, _, _, ok := m.Asset("good", "missing.js"); ok {
		t.Fatal("missing file")
	}
	if _, _, _, ok := m.Asset("nobody", "index.html"); ok {
		t.Fatal("missing plugin")
	}
}

func TestSettingsPageValidate(t *testing.T) {
	with := func(p string) error { m := baseMeta("x"); m.Settings = p; return m.Validate() }
	if err := with(""); err != nil {
		t.Fatalf("без страницы настроек: %v", err)
	}
	if err := with("settings.html"); err != nil {
		t.Fatalf("корректный путь: %v", err)
	}
	for _, bad := range []string{"../x.html", "/abs.html", "settings.js", "a/../../b.html", "settings.html?x=1"} {
		if err := with(bad); err == nil {
			t.Errorf("%q должен отклоняться", bad)
		}
	}
}
