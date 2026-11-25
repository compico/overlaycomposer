package wasmplugins

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeWasm(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func nopHandler(*fakeInst, string, []byte) ([]byte, error) { return nil, nil }

func newTestManager(t *testing.T, eng *fakeEngine, st StateStore, dirs ...string) *Manager {
	t.Helper()
	m := NewManager(context.Background(), Options{
		Engine: eng, Dirs: dirs, State: st, Services: Services{Log: quietLog()},
		Reserved: func(id string) bool { return id == "html-widget" },
	})
	t.Cleanup(m.Close)
	return m
}

func TestScanLoadsReloadsAndRemoves(t *testing.T) {
	dir := t.TempDir()
	a := &fakeModule{meta: baseMeta("alpha"), handler: nopHandler}
	b := &fakeModule{meta: baseMeta("beta"), handler: nopHandler}
	eng := &fakeEngine{mods: map[string]*fakeModule{"A1": a, "B1": b}}
	writeWasm(t, dir, "alpha.wasm", "A1")
	pb := writeWasm(t, dir, "beta.wasm", "B1")
	writeWasm(t, dir, "notes.txt", "ignored")
	m := newTestManager(t, eng, &MemState{}, dir, filepath.Join(dir, "missing"))

	m.Scan()
	infos, problems := m.List()
	if len(infos) != 2 || infos[0].Meta.ID != "alpha" || infos[1].Meta.ID != "beta" || len(problems) != 0 {
		t.Fatalf("scan: %+v %+v", infos, problems)
	}
	if infos[0].State != StateRunning || len(infos[0].SHA256) != 64 || infos[0].Granted == nil {
		t.Fatalf("info: %+v", infos[0])
	}
	before := eng.compileCount()
	m.Scan()
	if eng.compileCount() != before {
		t.Fatal("unchanged files must not be recompiled")
	}

	// файл изменился → перезапуск только его
	b2 := &fakeModule{meta: baseMeta("beta"), handler: nopHandler}
	eng.mods["B2"] = b2
	writeWasm(t, dir, "beta.wasm", "B2")
	m.Scan()
	if !b.closed || b2.count() != 2 || a.closed {
		t.Fatalf("only beta must be reloaded: a.closed=%v b.closed=%v b2=%d", a.closed, b.closed, b2.count())
	}
	// файл исчез → плагин остановлен
	if err := os.Remove(pb); err != nil {
		t.Fatal(err)
	}
	m.Scan()
	if m.Has("beta") || !m.Has("alpha") {
		t.Fatal("removed file must unload the plugin")
	}
}

func TestScanSadPaths(t *testing.T) {
	dir := t.TempDir()
	good := &fakeModule{meta: baseMeta("good"), handler: nopHandler}
	dup := &fakeModule{meta: baseMeta("good"), handler: nopHandler}
	clash := &fakeModule{meta: baseMeta("html-widget"), handler: nopHandler}
	badMeta := baseMeta("Bad_ID")
	badID := &fakeModule{meta: badMeta, handler: nopHandler}
	garbage := &fakeModule{rawMeta: []byte("{nope"), handler: nopHandler}
	empty := &fakeModule{rawMeta: []byte{}, handler: nopHandler}
	eng := &fakeEngine{mods: map[string]*fakeModule{"G": good, "D": dup, "C": clash, "I": badID, "J": garbage, "E": empty}}
	for n, body := range map[string]string{"1good.wasm": "G", "2dup.wasm": "D", "clash.wasm": "C", "badid.wasm": "I", "junk.wasm": "J", "empty.wasm": "E", "notwasm.wasm": "???"} {
		writeWasm(t, dir, n, body)
	}
	m := newTestManager(t, eng, nil, dir)
	m.Scan()
	infos, problems := m.List()
	if len(infos) != 1 || infos[0].Meta.ID != "good" || filepath.Base(infos[0].File) != "1good.wasm" {
		t.Fatalf("only the first valid file must load: %+v", infos)
	}
	if len(problems) != 6 {
		t.Fatalf("every bad file must be reported, got %d: %+v", len(problems), problems)
	}
	var msgs []string
	for _, p := range problems {
		msgs = append(msgs, filepath.Base(p.File)+": "+p.Error)
	}
	all := strings.Join(msgs, "\n")
	for _, want := range []string{"duplicate plugin id", "already used", "id \"Bad_ID\"", "oc_meta", "not a valid wasm"} {
		if !strings.Contains(all, want) {
			t.Errorf("problems must mention %q:\n%s", want, all)
		}
	}
	if !dup.closed {
		t.Fatal("the module of a duplicate must be released")
	}
}

func TestGrantsAndConfigPersist(t *testing.T) {
	dir := t.TempDir()
	meta := baseMeta("cfg")
	meta.Permissions = []string{PermStorage, PermTimer}
	meta.Subscriptions = []string{EventRedemption}
	meta.Permissions = append(meta.Permissions, PermRedeem)
	mod := &fakeModule{meta: meta, handler: nopHandler}
	eng := &fakeEngine{mods: map[string]*fakeModule{"C": mod}}
	writeWasm(t, dir, "cfg.wasm", "C")
	st := &MemState{}
	m := newTestManager(t, eng, st, dir)
	m.Scan()

	if m.Wants(EventRedemption) {
		t.Fatal("not granted: the host must not need EventSub for it")
	}
	n0 := mod.count()
	if err := m.SetGrants("cfg", []string{PermStorage, PermRedeem, PermStorage}); err != nil {
		t.Fatal(err)
	}
	if mod.count() != n0+1 {
		t.Fatal("a changed set of grants must restart the plugin so that oc_init runs with the new rights")
	}
	if err := m.SetGrants("cfg", []string{PermRedeem, PermStorage}); err != nil {
		t.Fatal(err)
	}
	if mod.count() != n0+1 {
		t.Fatal("the same set of grants (in another order) must not restart the plugin")
	}
	if !m.Wants(EventRedemption) {
		t.Fatal("granted and subscribed: must be wanted")
	}
	if n := m.Publish(EventRedemption, map[string]string{"id": "1"}); n != 1 {
		t.Fatalf("publish delivered to %d", n)
	}
	// sad path
	if err := m.SetGrants("cfg", []string{"root"}); err == nil {
		t.Fatal("unknown permission")
	}
	if err := m.SetGrants("cfg", []string{PermNetHTTP}); err == nil {
		t.Fatal("a permission the plugin did not request")
	}
	if err := m.SetGrants("nope", nil); err != ErrNoPlugin {
		t.Fatalf("got %v", err)
	}
	for _, bad := range []string{`[1]`, `"str"`, `null`, `{oops`, `{"a":"` + strings.Repeat("x", maxConfigBytes) + `"}`} {
		if err := m.SetConfig("cfg", json.RawMessage(bad)); err == nil {
			t.Errorf("config must be rejected: %.20s", bad)
		}
	}
	inst := len(mod.instances)
	if err := m.SetConfig("cfg", json.RawMessage(`{"token":"abc"}`)); err != nil {
		t.Fatal(err)
	}
	if len(mod.instances) != inst+1 || string(mod.instances[len(mod.instances)-1].config) != `{"token":"abc"}` {
		t.Fatal("new config must restart the instance and reach oc_init")
	}
	saved, _ := st.Load("cfg")
	if len(saved.Granted) != 2 || string(saved.Config) != `{"token":"abc"}` {
		t.Fatalf("state must persist: %+v", saved)
	}

	// «перезапуск приложения»: новый менеджер поднимает те же права и настройки
	m2 := newTestManager(t, &fakeEngine{mods: map[string]*fakeModule{"C": {meta: meta, handler: nopHandler}}}, st, dir)
	m2.Scan()
	infos, _ := m2.List()
	if len(infos[0].Granted) != 2 || string(infos[0].Config) != `{"token":"abc"}` {
		t.Fatalf("after restart: %+v", infos[0])
	}
}

func TestMetaValidate(t *testing.T) {
	good := baseMeta("ok")
	good.Permissions = []string{PermRedeem}
	good.Subscriptions = []string{EventRedemption}
	good.Routes = []Route{{"GET", "/", "root"}, {"POST", "/a/b-c", "h"}}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(m *Meta){
		"id upper":         func(m *Meta) { m.ID = "Up" },
		"id empty":         func(m *Meta) { m.ID = "" },
		"id leading dash":  func(m *Meta) { m.ID = "-a" },
		"name empty":       func(m *Meta) { m.Name = " " },
		"version":          func(m *Meta) { m.Version = "1.0" },
		"api version":      func(m *Meta) { m.APIVersion = 2 },
		"unknown perm":     func(m *Meta) { m.Permissions = []string{"fs"} },
		"dup perm":         func(m *Meta) { m.Permissions = []string{PermRedeem, PermRedeem} },
		"sub without perm": func(m *Meta) { m.Permissions = nil },
		"unknown sub":      func(m *Meta) { m.Subscriptions = []string{"x"} },
		"dup sub":          func(m *Meta) { m.Subscriptions = []string{EventRedemption, EventRedemption} },
		"method":           func(m *Meta) { m.Routes = []Route{{"TRACE", "/x", "h"}} },
		"path no slash":    func(m *Meta) { m.Routes = []Route{{"GET", "x", "h"}} },
		"path traversal":   func(m *Meta) { m.Routes = []Route{{"GET", "/a/../b", "h"}} },
		"path trailing":    func(m *Meta) { m.Routes = []Route{{"GET", "/a/", "h"}} },
		"path chars":       func(m *Meta) { m.Routes = []Route{{"GET", "/a b", "h"}} },
		"handler on_event": func(m *Meta) { m.Routes = []Route{{"GET", "/x", "on_event"}} },
		"handler chars":    func(m *Meta) { m.Routes = []Route{{"GET", "/x", "a-b"}} },
		"duplicate route":  func(m *Meta) { m.Routes = []Route{{"GET", "/x", "a"}, {"GET", "/x", "b"}} },
		"too many routes":  func(m *Meta) { m.Routes = make([]Route, maxRoutes+1) },
		"cmd sub no perm": func(m *Meta) {
			m.Permissions = nil
			m.Subscriptions = []string{EventChatCommand}
			m.Commands = []string{"np"}
		},
		"cmd sub no cmds": func(m *Meta) {
			m.Permissions = []string{PermRedeem, PermChatCommandRead}
			m.Subscriptions = []string{EventChatCommand}
		},
		"action bad id":     func(m *Meta) { m.Actions = []Action{{ID: "Bad", Title: "t", Path: "/a"}} },
		"action no title":   func(m *Meta) { m.Actions = []Action{{ID: "a", Title: " ", Path: "/a"}} },
		"action no route":   func(m *Meta) { m.Actions = []Action{{ID: "a", Title: "t", Path: "/nope"}} },
		"action wrong verb": func(m *Meta) { m.Actions = []Action{{ID: "a", Title: "t", Method: "GET", Path: "/a/b-c"}} },
		"action bad body":   func(m *Meta) { m.Actions = []Action{{ID: "a", Title: "t", Path: "/a/b-c", Body: []byte("{")}} },
		"action big body": func(m *Meta) {
			m.Actions = []Action{{ID: "a", Title: "t", Path: "/a/b-c", Body: []byte(`"` + strings.Repeat("x", maxActionBody) + `"`)}}
		},
		"action dup": func(m *Meta) {
			m.Actions = []Action{{ID: "a", Title: "t", Path: "/a/b-c"}, {ID: "a", Title: "u", Path: "/a/b-c"}}
		},
		"too many actions": func(m *Meta) { m.Actions = make([]Action, maxActions+1) },
		"cmds no sub":      func(m *Meta) { m.Commands = []string{"np"} },
		"cmd bad name": func(m *Meta) {
			m.Permissions = []string{PermChatCommandRead}
			m.Subscriptions = []string{EventChatCommand}
			m.Commands = []string{"!np"}
		},
		"cmd upper": func(m *Meta) {
			m.Permissions = []string{PermChatCommandRead}
			m.Subscriptions = []string{EventChatCommand}
			m.Commands = []string{"NP"}
		},
		"cmd dup": func(m *Meta) {
			m.Permissions = []string{PermChatCommandRead}
			m.Subscriptions = []string{EventChatCommand}
			m.Commands = []string{"np", "np"}
		},
	}
	for name, mut := range cases {
		m := good
		m.Routes = append([]Route(nil), good.Routes...)
		mut(&m)
		if m.Validate() == nil {
			t.Errorf("%s: must be rejected", name)
		}
	}
}

// Общая папка плагинов: рядом с .wasm лежат подпапки HTML-плагинов и прочие файлы — менеджер берёт только файлы .wasm.
func TestScanIgnoresHTMLPluginFoldersAndOtherFiles(t *testing.T) {
	dir := t.TempDir()
	writeWasm(t, dir, "good.wasm", "GOOD")
	if err := os.MkdirAll(filepath.Join(dir, "html-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeWasm(t, filepath.Join(dir, "html-plugin"), "nested.wasm", "NESTED") // внутри подпапки — не наш
	writeWasm(t, filepath.Join(dir, "html-plugin"), "plugin.json", `{"id":"html-plugin"}`)
	writeWasm(t, dir, "readme.txt", "x")
	writeWasm(t, dir, ".hidden.wasm", "HIDDEN")
	eng := &fakeEngine{mods: map[string]*fakeModule{
		"GOOD":   {meta: baseMeta("good"), handler: nopHandler},
		"NESTED": {meta: baseMeta("nested"), handler: nopHandler},
		"HIDDEN": {meta: baseMeta("hidden"), handler: nopHandler},
	}}
	m := newTestManager(t, eng, &MemState{}, dir)
	m.Scan()
	infos, problems := m.List()
	if len(infos) != 1 || infos[0].Meta.ID != "good" || len(problems) != 0 {
		t.Fatalf("infos=%+v problems=%+v", infos, problems)
	}
}

func TestMetaValidateChatCommands(t *testing.T) {
	m := baseMeta("cmd")
	m.Permissions = []string{PermChatCommandRead}
	m.Subscriptions = []string{EventChatCommand}
	m.Commands = []string{"np", "info_2"}
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
	if !m.WantsCommand("np") || m.WantsCommand("other") {
		t.Fatal("WantsCommand")
	}
}

func TestMetaValidateChatMessages(t *testing.T) {
	m := baseMeta("chat")
	m.Subscriptions = []string{EventChatMessage}
	if m.Validate() == nil {
		t.Fatal("подписка chat.message без разрешения chat.read должна отклоняться")
	}
	m.Permissions = []string{PermChatRead}
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
	// Разрешение chat.read не даёт команд и не требует Commands: это разные права.
	if m.WantsCommand("np") {
		t.Fatal("chat.read не объявляет команд")
	}
	if !KnownPermission(PermChatRead) {
		t.Fatal("chat.read должно быть известным правом")
	}
}

func TestMetaActions(t *testing.T) {
	m := baseMeta("music")
	m.Routes = []Route{{"POST", "/control", "ctl"}}
	m.Actions = []Action{
		{ID: "pause", Title: "Pause", Icon: "pause", Path: "/control", Body: []byte(`{"action":"pause"}`)},
		{ID: "skip-ban", Title: "Skip and ban", Path: "/control", Body: []byte(`{"action":"skip_ban"}`), Danger: true},
	}
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
	if m.Actions[0].Method != "POST" { // метод по умолчанию заполняется при проверке
		t.Fatalf("method %q", m.Actions[0].Method)
	}
	if a, ok := m.FindAction("skip-ban"); !ok || !a.Danger || string(a.Body) != `{"action":"skip_ban"}` {
		t.Fatalf("%+v %v", a, ok)
	}
	if _, ok := m.FindAction("nope"); ok {
		t.Fatal("несуществующее действие")
	}
}

func TestActionActiveWhen(t *testing.T) {
	m := baseMeta("music")
	m.Routes = []Route{{"POST", "/control", "ctl"}}
	m.Events = []EventDecl{{ID: "np"}}
	ok := func(w *ActiveWhen) error {
		m.Actions = []Action{{ID: "toggle", Title: "T", Path: "/control", ActiveWhen: w}}
		return m.Validate()
	}
	if err := ok(&ActiveWhen{Topic: "np", Field: "playing", Equals: []byte(`true`)}); err != nil {
		t.Fatal(err)
	}
	for name, w := range map[string]*ActiveWhen{
		"топик не объявлен": {Topic: "other"},
		"плохое поле":       {Topic: "np", Field: "a b"},
		"глубокое поле":     {Topic: "np", Field: "a.b.c.d.e"},
		"не скаляр":         {Topic: "np", Equals: []byte(`{"a":1}`)},
		"не JSON":           {Topic: "np", Equals: []byte(`nope`)},
	} {
		if ok(w) == nil {
			t.Errorf("%s: ожидалась ошибка", name)
		}
	}
	cases := []struct {
		w    ActiveWhen
		data string
		want bool
	}{
		{ActiveWhen{Topic: "np", Field: "playing", Equals: []byte(`true`)}, `{"playing":true}`, true},
		{ActiveWhen{Topic: "np", Field: "playing", Equals: []byte(`true`)}, `{"playing":false}`, false},
		{ActiveWhen{Topic: "np", Field: "playing"}, `{"playing":true}`, true},
		{ActiveWhen{Topic: "np", Field: "a.b", Equals: []byte(`"x"`)}, `{"a":{"b":"x"}}`, true},
		{ActiveWhen{Topic: "np", Field: "a.b"}, `{"a":1}`, false},
		{ActiveWhen{Topic: "np", Field: "n", Equals: []byte(`3`)}, `{"n":3}`, true},
		{ActiveWhen{Topic: "np"}, `true`, true},
		{ActiveWhen{Topic: "np"}, `""`, false},
		{ActiveWhen{Topic: "np"}, `null`, false},
		{ActiveWhen{Topic: "np"}, `not json`, false},
		{ActiveWhen{Topic: "np", Field: "x"}, `[1]`, false},
	}
	for _, c := range cases {
		if got := c.w.Match([]byte(c.data)); got != c.want {
			t.Errorf("%+v на %s: %v", c.w, c.data, got)
		}
	}
}

func TestActionDisplay(t *testing.T) {
	m := baseMeta("music")
	m.Routes = []Route{{"POST", "/control", "ctl"}}
	m.Events = []EventDecl{{ID: "now"}}
	check := func(d *Display) error {
		m.Actions = []Action{{ID: "now", Title: "Now", Path: "/control", Display: d}}
		return m.Validate()
	}
	if err := check(&Display{Topic: "now", Fields: []string{"now.title", "now.artists"}}); err != nil {
		t.Fatal(err)
	}
	for name, d := range map[string]*Display{
		"топик не объявлен": {Topic: "x", Fields: []string{"a"}},
		"нет полей":         {Topic: "now"},
		"много полей":       {Topic: "now", Fields: []string{"a", "b", "c", "d", "e"}},
		"плохое поле":       {Topic: "now", Fields: []string{"a b"}},
	} {
		if check(d) == nil {
			t.Errorf("%s: ожидалась ошибка", name)
		}
	}
	d := Display{Topic: "now", Fields: []string{"now.title", "now.artists", "now.n", "missing", "now.empty"}}
	got := d.Lines([]byte(`{"now":{"title":"  Song \n Name ","artists":["A"," ","B"],"n":2.5,"empty":""}}`))
	if want := []string{"Song Name", "A, B", "2.5"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("%q", got)
	}
	long := Display{Topic: "now", Fields: []string{"t"}}.Lines([]byte(`{"t":"` + strings.Repeat("я", 200) + `"}`))
	if len(long) != 1 || len([]rune(long[0])) != maxDisplayRunes {
		t.Fatalf("обрезка: %d", len([]rune(long[0])))
	}
	if (Display{Fields: []string{"a"}}).Lines([]byte(`nope`)) != nil {
		t.Fatal("не JSON")
	}
}

func TestInspectChecksWithoutRegistering(t *testing.T) {
	dir := t.TempDir()
	mod := &fakeModule{meta: baseMeta("alpha"), handler: nopHandler}
	eng := &fakeEngine{mods: map[string]*fakeModule{"A1": mod}}
	m := newTestManager(t, eng, &MemState{}, dir)
	meta, err := m.Inspect([]byte("A1"))
	if err != nil || meta.ID != "alpha" {
		t.Fatalf("%+v %v", meta, err)
	}
	if infos, _ := m.List(); len(infos) != 0 {
		t.Fatalf("Inspect ничего не регистрирует: %+v", infos)
	}
	if _, err := m.Inspect([]byte("nope")); err == nil {
		t.Fatal("не модуль")
	}
	if _, err := m.Inspect(nil); err == nil {
		t.Fatal("пустой файл")
	}
}

func TestMinHost(t *testing.T) {
	m := baseMeta("alpha")
	m.MinHost = "v2026.11.25.7"
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
	m.MinHost = "2026.11"
	if m.Validate() == nil {
		t.Fatal("minHost не в формате тега релиза должен отклоняться")
	}

	// Инспекция: хост старее — понятная ошибка раньше «unknown permission»; новее, равный и dev — проверки нет.
	nm := baseMeta("alpha")
	nm.MinHost = "v2026.11.25.7"
	nm.Permissions = []string{"from.the.future"}
	eng := &fakeEngine{mods: map[string]*fakeModule{"A1": {meta: nm, handler: nopHandler}}}
	check := func(host string) error {
		mgr := NewManager(context.Background(), Options{Engine: eng, Dirs: []string{t.TempDir()}, State: &MemState{}, Services: Services{Log: quietLog()}, HostVersion: host})
		defer mgr.Close()
		_, err := mgr.Inspect([]byte("A1"))
		return err
	}
	var tooOld *HostTooOldError
	if err := check("v2026.11.23.6"); !errors.As(err, &tooOld) || tooOld.Need != "v2026.11.25.7" || tooOld.Have != "v2026.11.23.6" {
		t.Fatalf("старый хост: %v", err)
	}
	for _, host := range []string{"v2026.11.25.7", "v2026.12.01.8", "dev", ""} {
		err := check(host)
		if errors.As(err, &tooOld) {
			t.Fatalf("хост %q не старее: %v", host, err)
		}
		if err == nil || !strings.Contains(err.Error(), "unknown permission") {
			t.Fatalf("хост %q: ждали обычную проверку меты, получили %v", host, err)
		}
	}
}
