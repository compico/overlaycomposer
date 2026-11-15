package wasmplugins

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// localFetch запускает плагин с заданными метаданными и настройками, делает один Fetch на rawURL и возвращает либо отказ хоста
// (ошибка вызова), либо ответ, пришедший событием.
func localFetch(t *testing.T, meta Meta, cfg string, rawURL string, grants ...string) (denied string, res fetchResult) {
	t.Helper()
	meta.Routes = []Route{{"GET", "/go", "go"}}
	resCh := make(chan fetchResult, 1)
	deniedCh := make(chan string, 1)
	mod := &fakeModule{meta: meta, handler: func(in *fakeInst, name string, arg []byte) ([]byte, error) {
		if name == "go" {
			r := in.hc("http.fetch", map[string]any{"id": "r1", "url": rawURL})
			e, _ := r["error"].(string)
			deniedCh <- e
			return nil, nil
		}
		var ev struct {
			Type string      `json:"type"`
			Data fetchResult `json:"data"`
		}
		_ = json.Unmarshal(arg, &ev)
		if ev.Type == EventHTTPResponse {
			resCh <- ev.Data
		}
		return nil, nil
	}}
	var raw json.RawMessage
	if cfg != "" {
		raw = json.RawMessage(cfg)
	}
	p := newPlugin(context.Background(), Services{Log: quietLog(), FetchLocal: newLocalClient()}, mod, mod.meta, "x.wasm", "sha", raw, grants)
	if err := p.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Stop() })
	if _, err := p.Handle(context.Background(), Request{Method: "GET", Path: "/go"}); err != nil {
		t.Fatal(err)
	}
	if e := <-deniedCh; e != "" {
		return e, fetchResult{}
	}
	select {
	case res = <-resCh:
	case <-time.After(5 * time.Second):
		t.Fatal("no http.response event")
	}
	return "", res
}

func localMeta(target, setting string, perms ...string) Meta {
	m := baseMeta("localfetch")
	m.Permissions = perms
	m.NetLocal = []NetLocalTarget{{Target: target, Setting: setting}}
	return m
}

func hostOf(srv *httptest.Server) string { return strings.TrimPrefix(srv.URL, "http://") }

func TestNetLocalFetchesDeclaredTarget(t *testing.T) {
	big := strings.Repeat("a", 2<<20) // больше обычного предела 1 МиБ
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/redirect":
			http.Redirect(w, r, "http://169.254.169.254/", http.StatusFound)
		default:
			_, _ = w.Write([]byte(big))
		}
	}))
	defer srv.Close()
	meta := localMeta(hostOf(srv), "", PermNetLocal)

	denied, r := localFetch(t, meta, "", srv.URL+"/files/beatmap/x.osu", PermNetLocal)
	if denied != "" || r.Status != 200 || len(r.Body) != len(big) {
		t.Fatalf("declared target: denied=%q status=%d len=%d err=%q", denied, r.Status, len(r.Body), r.Error)
	}
	// Редирект не выполняется: ответ 302 приходит как есть, на другой адрес хост не идёт.
	denied, r = localFetch(t, meta, "", srv.URL+"/redirect", PermNetLocal)
	if denied != "" || r.Status != http.StatusFound {
		t.Fatalf("redirect must not be followed: denied=%q status=%d err=%q", denied, r.Status, r.Error)
	}
}

func TestNetLocalNeedsItsOwnGrant(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("x")) }))
	defer srv.Close()
	meta := localMeta(hostOf(srv), "", PermNetLocal, PermNetHTTP)

	// net.http не заменяет net.local для объявленной цели.
	if denied, _ := localFetch(t, meta, "", srv.URL, PermNetHTTP); !strings.Contains(denied, "net.local") {
		t.Fatalf("want permission denied: net.local, got %q", denied)
	}
	// Без выдачи вообще — тоже отказ.
	if denied, _ := localFetch(t, meta, "", srv.URL); !strings.Contains(denied, "net.local") {
		t.Fatalf("want permission denied: net.local, got %q", denied)
	}
}

func TestNetLocalDoesNotOpenOtherLocalAddresses(t *testing.T) {
	declared := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer declared.Close()
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("secret")) }))
	defer other.Close()
	meta := localMeta(hostOf(declared), "", PermNetLocal, PermNetHTTP)

	// Другой порт на этом же компьютере: с net.local без net.http — отказ по net.http.
	if denied, _ := localFetch(t, meta, "", other.URL, PermNetLocal); !strings.Contains(denied, "net.http") {
		t.Fatalf("undeclared local address must need net.http, got %q", denied)
	}
	// С обоими разрешениями — запрос идёт обычным клиентом, а он закрыт от локальной сети: ошибка приходит событием.
	meta.Permissions = []string{PermNetLocal, PermNetHTTP}
	denied, r := localFetch(t, meta, "", other.URL, PermNetLocal, PermNetHTTP)
	if denied != "" || r.Status != 0 || r.Error == "" {
		t.Fatalf("public client must not reach loopback: denied=%q %+v", denied, r)
	}
}

func TestNetLocalSettingOverridesTarget(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }))
	defer srv.Close()
	meta := localMeta("127.0.0.1:1", "tosuHost", PermNetLocal)

	// Настройка заменяет адрес.
	denied, r := localFetch(t, meta, `{"tosuHost":"`+hostOf(srv)+`"}`, srv.URL, PermNetLocal)
	if denied != "" || r.Status != 200 || r.Body != "ok" {
		t.Fatalf("setting target: denied=%q %+v", denied, r)
	}
	// Без настройки действует адрес по умолчанию, и сервер на другом порту не открывается.
	if denied, _ := localFetch(t, meta, "", srv.URL, PermNetLocal); denied == "" {
		t.Fatal("default target must not match another port")
	}
	// Нелокальный адрес в настройке никуда не пускает.
	if denied, _ := localFetch(t, meta, `{"tosuHost":"192.168.1.5:80"}`, "http://192.168.1.5:80/", PermNetLocal); denied == "" {
		t.Fatal("non-loopback setting must be ignored")
	}
}

func TestNormLocalTargetAndURLHostPort(t *testing.T) {
	for in, want := range map[string]string{
		"127.0.0.1:24050": "127.0.0.1:24050", "LOCALHOST:80": "localhost:80", "[::1]:8080": "[::1]:8080",
	} {
		if got, ok := normLocalTarget(in); !ok || got != want {
			t.Errorf("normLocalTarget(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "127.0.0.1", "127.0.0.1:0", "127.0.0.1:70000", "127.0.0.1:080", "192.168.1.5:80", "0.0.0.0:80",
		"example.com:80", "127.0.0.1.evil.com:80", "evil.com@127.0.0.1:80"} {
		if _, ok := normLocalTarget(in); ok {
			t.Errorf("normLocalTarget(%q) must be rejected", in)
		}
	}
	if got := urlHostPort("http://127.0.0.1:24050/files/x"); got != "127.0.0.1:24050" {
		t.Errorf("urlHostPort = %q", got)
	}
	// Хитрые адреса не превращаются в локальную цель.
	for _, in := range []string{"http://127.0.0.1:24050@evil.com/", "http://127.0.0.1.evil.com:24050/", "http://evil.com/?127.0.0.1:24050"} {
		if got := urlHostPort(in); got != "" {
			t.Errorf("urlHostPort(%q) = %q, want empty", in, got)
		}
	}
	// https к цели net.local не пускаем: у локальной программы нет сертификата.
	p := &Plugin{meta: localMeta("127.0.0.1:24050", "", PermNetLocal), cfg: json.RawMessage(`{}`)}
	if !p.isLocalTarget("http://127.0.0.1:24050/a") || p.isLocalTarget("https://127.0.0.1:24050/a") {
		t.Error("isLocalTarget: only http to the declared target")
	}
}

func TestMetaValidateNetLocal(t *testing.T) {
	good := func() Meta { return localMeta("127.0.0.1:24050", "tosuHost", PermNetLocal) }
	if m := good(); m.Validate() != nil {
		t.Fatalf("good meta: %v", m.Validate())
	}
	cases := map[string]func(*Meta){
		"netLocal без разрешения": func(m *Meta) { m.Permissions = nil },
		"разрешение без целей":    func(m *Meta) { m.NetLocal = nil },
		"нелокальный адрес":       func(m *Meta) { m.NetLocal[0].Target = "192.168.1.5:80" },
		"домен":                   func(m *Meta) { m.NetLocal[0].Target = "example.com:80" },
		"без порта":               func(m *Meta) { m.NetLocal[0].Target = "127.0.0.1" },
		"плохое имя настройки":    func(m *Meta) { m.NetLocal[0].Setting = "a b" },
		"слишком много целей": func(m *Meta) {
			for i := 0; i < maxNetLocal; i++ {
				m.NetLocal = append(m.NetLocal, NetLocalTarget{Target: "127.0.0.1:8000"})
			}
		},
	}
	for name, mutate := range cases {
		m := good()
		mutate(&m)
		if err := m.Validate(); err == nil {
			t.Errorf("%s: Validate must fail", name)
		}
	}
}

func TestNetLocalPathPrefix(t *testing.T) {
	m := localMeta("127.0.0.1:24050", "", PermNetLocal)
	m.NetLocal[0].Paths = []string{"/files/beatmap/"}
	p := &Plugin{meta: m, cfg: json.RawMessage(`{}`)}
	base := "http://127.0.0.1:24050"
	for path, want := range map[string]bool{
		"/files/beatmap/123 a - b/a - b [x].osu": true,
		"/files/beatmap/123/Song... [Hard].osu":  true, // многоточие в названии — не «..»
		"/files/beatmap/123%5Cfile.osu":          true,
		"/json/v2":                               false,
		"/files/beatmap/../../json/v2":           false,
		"/files/beatmap/%2e%2e/json":             false,
		"/files/beatmap/..%5cjson":               false,
		"/files/beatmap/123/../../x":             false,
		"/files/beatmapx":                        false,
		"/":                                      false,
	} {
		if got := p.isLocalTarget(base + path); got != want {
			t.Errorf("%s: isLocalTarget = %v, want %v", path, got, want)
		}
	}
	for _, bad := range []string{"files/", "/a/../b", "/a\\..\\b"} {
		mm := localMeta("127.0.0.1:24050", "", PermNetLocal)
		mm.NetLocal[0].Paths = []string{bad}
		if mm.Validate() == nil {
			t.Errorf("paths %q must be rejected by Validate", bad)
		}
	}
}
