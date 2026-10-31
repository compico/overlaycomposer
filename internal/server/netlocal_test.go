package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/compico/overlaycomposer/internal/config"
	"github.com/compico/overlaycomposer/internal/model"
	"github.com/compico/overlaycomposer/internal/plugins"
	"github.com/compico/overlaycomposer/internal/store"
)

func upstream(t *testing.T, name string) (*httptest.Server, string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, name+":"+r.URL.Path) }))
	t.Cleanup(srv.Close)
	return srv, strings.TrimPrefix(srv.URL, "http://")
}

func TestPluginNetLocalPolicy(t *testing.T) {
	_, tosu := upstream(t, "tosu")
	_, other := upstream(t, "other")
	ocSrv, oc := upstream(t, "oc") // «сам OC»: порт закрыт для плагинов всегда
	ocPort, _ := strconv.Atoi(strings.Split(oc, ":")[1])
	_ = ocSrv

	manifest := fmt.Sprintf(`{"id":"np","name":"NP","permissions":["net.local"],
	 "settings":[{"key":"host","type":"string","default":%q}],
	 "netLocal":[{"setting":"host","paths":["/json/"]},{"target":%q,"paths":["/api/"]}]}`, tosu, oc)
	reg := plugins.New(fstest.MapFS{
		"np/plugin.json":   {Data: []byte(manifest)},
		"np/index.html":    {Data: []byte("x")},
		"bare/plugin.json": {Data: []byte(`{"id":"bare","name":"Bare","permissions":["net.local"]}`)},
		"bare/index.html":  {Data: []byte("x")},
	}, nil)
	st, err := store.Open(filepath.Join(t.TempDir(), "oc.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	save := func(id, plugin string, granted []string, settings map[string]any) {
		t.Helper()
		if err := st.SaveSource(model.Source{ID: id, Name: id, Type: "plugin", Data: model.SourceData{Plugin: plugin, Granted: granted, Settings: settings}}); err != nil {
			t.Fatal(err)
		}
	}
	save("w-ok", "np", []string{"net.local"}, nil)
	save("w-nogrant", "np", []string{"chat.read"}, nil)
	save("w-moved", "np", []string{"net.local"}, map[string]any{"host": other})
	save("w-bare", "bare", []string{"net.local"}, nil)
	save("w-rate", "np", []string{"net.local"}, nil)

	s := &Server{d: Deps{Store: st, Plugins: reg, Running: config.Config{Port: ocPort}}}
	do := func(source, plugin, target, path string) (string, error) {
		r, err := s.netGet(netReq{Source: source, Plugin: plugin, Target: target, Path: path})
		return r.Body, err
	}

	// настройка по умолчанию из манифеста
	if body, err := do("w-ok", "np", tosu, "/json/v2"); err != nil || body != "tosu:/json/v2" {
		t.Fatalf("ok: %q %v", body, err)
	}
	// пользователь сменил адрес в настройках виджета — действует новый, прежний больше не разрешён
	if body, err := do("w-moved", "np", other, "/json/x"); err != nil || body != "other:/json/x" {
		t.Fatalf("moved: %q %v", body, err)
	}
	if _, err := do("w-moved", "np", tosu, "/json/x"); err == nil {
		t.Error("прежний адрес после смены настройки должен быть закрыт")
	}

	// sad paths
	for name, c := range map[string]struct{ source, plugin, target, path string }{
		"нет разрешения net.local у виджета": {"w-nogrant", "np", tosu, "/json/v2"},
		"путь вне префиксов":                 {"w-ok", "np", tosu, "/admin"},
		"чужой порт":                         {"w-ok", "np", other, "/json/v2"},
		"выход из префикса":                  {"w-ok", "np", tosu, "/json/../admin"},
		"виджет чужого плагина":              {"w-ok", "bare", tosu, "/json/v2"},
		"неизвестный виджет":                 {"nope", "np", tosu, "/json/v2"},
		"виджет не плагин / пустой id":       {"w-ok", "", tosu, "/json/v2"},
		"плагин без netLocal":                {"w-bare", "bare", tosu, "/json/v2"},
		"порт самого OC, даже если объявлен": {"w-ok", "np", oc, "/api/state"},
	} {
		if body, err := do(c.source, c.plugin, c.target, c.path); err == nil {
			t.Errorf("%s: должно быть отказано, получили %q", name, body)
		}
	}

	// частота: 60 запросов за окно, дальше отказ
	var last error
	for i := 0; i < 65; i++ {
		_, last = do("w-rate", "np", tosu, "/json/v2")
		if last != nil {
			if i < 60 {
				t.Fatalf("отказ слишком рано (%d): %v", i, last)
			}
			break
		}
	}
	if last == nil || !strings.Contains(last.Error(), "too many") {
		t.Fatalf("лимит частоты не сработал: %v", last)
	}
}
