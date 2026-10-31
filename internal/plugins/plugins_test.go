package plugins

import (
	"encoding/json"
	"io"
	"strings"
	"testing"
	"testing/fstest"
)

func mfs(files map[string]string) fstest.MapFS {
	m := fstest.MapFS{}
	for k, v := range files {
		m[k] = &fstest.MapFile{Data: []byte(v)}
	}
	return m
}

const okManifest = `{"id":"demo","name":"Demo","settings":[
 {"key":"channel","type":"string"},
 {"key":"size","type":"number","min":8,"max":100,"default":20},
 {"key":"mode","type":"select","options":[{"value":"a","label":"A"},{"value":"b","label":"B"}]},
 {"key":"color","type":"color"},
 {"key":"on","type":"bool"}]}`

func TestBuiltinPluginsAreValid(t *testing.T) {
	r := New(Builtin(), nil)
	ids := []string{}
	for _, m := range r.List() {
		ids = append(ids, m.ID)
		if !m.Builtin {
			t.Errorf("%s: Builtin must be true", m.ID)
		}
	}
	if !strings.Contains(strings.Join(ids, ","), "twitch-chat") {
		t.Fatalf("встроенный twitch-chat не найден: %v", ids)
	}
	p, _ := r.Get("twitch-chat")
	f, name, err := p.Open("")
	if err != nil || name != "index.html" {
		t.Fatalf("Open(\"\"): %v %q", err, name)
	}
	b, _ := io.ReadAll(f)
	f.Close()
	if !strings.Contains(string(b), "irc-ws.chat.twitch.tv") {
		t.Error("index.html чата повреждён")
	}
	// все ключи настроек, которые читает виджет, должны быть в манифесте (и наоборот)
	for _, s := range p.Settings {
		if !strings.Contains(string(b), `"`+s.Key+`"`) {
			t.Errorf("настройка %q не читается виджетом", s.Key)
		}
	}
}

func TestRegistryUserPlugins(t *testing.T) {
	user := mfs(map[string]string{
		"demo/plugin.json":        okManifest,
		"demo/index.html":         "<p>hi",
		"demo/js/app.js":          "1",
		"twitch-chat/plugin.json": `{"id":"twitch-chat","name":"Fake"}`, // id занят встроенным
		"twitch-chat/index.html":  "x",
		"broken/plugin.json":      `{not json`,
		"noentry/plugin.json":     `{"id":"noentry","name":"N"}`,
		"badkey/plugin.json":      `{"id":"badkey","name":"B","settings":[{"key":"1x","type":"string"}]}`,
		"badkey/index.html":       "x",
	})
	r := New(Builtin(), user)
	var ids []string
	for _, m := range r.List() {
		ids = append(ids, m.ID)
	}
	if got := strings.Join(ids, ","); got != "twitch-chat,demo" {
		t.Fatalf("ожидалось twitch-chat,demo (встроенные первыми, битые пропущены), got %s", got)
	}
	if p, _ := r.Get("twitch-chat"); !p.Builtin {
		t.Error("пользовательский плагин не должен подменять встроенный")
	}
	p, ok := r.Get("demo")
	if !ok {
		t.Fatal("demo не найден")
	}
	for _, bad := range []string{"../demo/plugin.json", "/etc/passwd", "js", "nope.html", "..\\x"} {
		if f, _, err := p.Open(bad); err == nil {
			f.Close()
			t.Errorf("Open(%q) должен отказать", bad)
		}
	}
	if f, name, err := p.Open("/js/app.js"); err != nil || name != "js/app.js" {
		t.Errorf("Open js: %v %q", err, name)
	} else {
		f.Close()
	}
	if p.DefaultSize.Width != 400 || p.Entry != "index.html" {
		t.Errorf("значения по умолчанию: %+v", p.Manifest)
	}
}

func TestRegistryRescan(t *testing.T) {
	user := mfs(nil)
	r := New(nil, user)
	if _, ok := r.Get("late"); ok {
		t.Fatal("плагина ещё нет")
	}
	user["late/plugin.json"] = &fstest.MapFile{Data: []byte(`{"id":"late","name":"L"}`)}
	user["late/index.html"] = &fstest.MapFile{Data: []byte("x")}
	if _, ok := r.Get("late"); !ok {
		t.Fatal("плагин, положенный в папку позже, должен находиться без перезапуска")
	}
}

func TestCleanSettings(t *testing.T) {
	r := New(nil, mfs(map[string]string{"demo/plugin.json": okManifest, "demo/index.html": "x"}))
	p, _ := r.Get("demo")
	got, err := p.CleanSettings(map[string]any{"channel": "shroud", "size": 500.0, "mode": "b", "color": "#ff00aa", "on": true, "alien": "x"})
	if err != nil {
		t.Fatal(err)
	}
	if got["size"] != 100.0 || got["channel"] != "shroud" || got["on"] != true || got["mode"] != "b" {
		t.Errorf("clean: %v", got)
	}
	if _, has := got["alien"]; has {
		t.Error("неизвестные настройки должны отбрасываться")
	}
	for name, in := range map[string]map[string]any{
		"select не из списка": {"mode": "z"},
		"цвет":                {"color": "red"},
		"тип bool":            {"on": "true"},
		"тип number":          {"size": "10"},
		"длина":               {"channel": strings.Repeat("я", 2001)},
	} {
		if _, err := p.CleanSettings(in); err == nil {
			t.Errorf("%s: ожидалась ошибка", name)
		}
	}
}

func TestPermissions(t *testing.T) {
	m := Manifest{ID: "p", Name: "P", Permissions: []string{PermChatRead, PermChatSend}}
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
	if (&Manifest{ID: "p", Name: "P", Permissions: []string{"chat.read", "root"}}).Validate() == nil {
		t.Fatal("unknown permission accepted")
	}
	if (&Manifest{ID: "p", Name: "P", Permissions: []string{"chat.read", "chat.read"}}).Validate() == nil {
		t.Fatal("duplicate permission accepted")
	}
	got, err := m.CleanGranted([]string{"chat.send", "chat.send"})
	if err != nil || len(got) != 1 || got[0] != "chat.send" {
		t.Fatalf("clean: %v %v", got, err)
	}
	if got, err := m.CleanGranted(nil); err != nil || got == nil || len(got) != 0 {
		t.Fatalf("empty: %#v %v", got, err)
	}
	only := Manifest{ID: "q", Name: "Q", Permissions: []string{PermChatRead}}
	if _, err := only.CleanGranted([]string{"chat.send"}); err == nil {
		t.Fatal("granted a permission the plugin did not request")
	}
}

func TestNetLocalManifest(t *testing.T) {
	base := func(rules string) *Manifest {
		var m Manifest
		src := `{"id":"p","name":"P","permissions":["net.local"],"settings":[{"key":"host","type":"string"},{"key":"n","type":"number"}],"netLocal":` + rules + `}`
		if err := json.Unmarshal([]byte(src), &m); err != nil {
			t.Fatal(err)
		}
		return &m
	}
	if err := base(`[{"setting":"host","paths":["/json/"]},{"target":"127.0.0.1:9000","paths":["/s"]}]`).Validate(); err != nil {
		t.Fatalf("валидный манифест: %v", err)
	}
	for name, rules := range map[string]string{
		"настройка не найдена": `[{"setting":"nope","paths":["/"]}]`,
		"настройка не строка":  `[{"setting":"n","paths":["/"]}]`,
		"не loopback":          `[{"target":"192.168.1.5:80","paths":["/"]}]`,
		"нет путей":            `[{"target":"127.0.0.1:80"}]`,
		"и target, и setting":  `[{"target":"127.0.0.1:80","setting":"host","paths":["/"]}]`,
		"выход вверх":          `[{"target":"127.0.0.1:80","paths":["/a/../b"]}]`,
		"слишком много правил": `[` + strings.Repeat(`{"target":"127.0.0.1:80","paths":["/"]},`, 8) + `{"target":"127.0.0.1:80","paths":["/"]}]`,
	} {
		if err := base(rules).Validate(); err == nil {
			t.Errorf("%s: манифест должен быть отвергнут", name)
		}
	}
	// netLocal без разрешения net.local бессмыслен
	m := base(`[{"target":"127.0.0.1:80","paths":["/"]}]`)
	m.Permissions = nil
	if m.Validate() == nil {
		t.Error("netLocal без разрешения net.local")
	}
}
