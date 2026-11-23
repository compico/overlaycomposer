package market

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

type fakeReleases struct {
	list   []Release
	files  map[int64]string
	err    error
	listed atomic.Int32
	loaded atomic.Int32
}

func (f *fakeReleases) List(context.Context, Repo, string) ([]Release, error) {
	f.listed.Add(1)
	return f.list, f.err
}

func (f *fakeReleases) Asset(_ context.Context, _ Repo, _ string, id int64, max int64) ([]byte, error) {
	f.loaded.Add(1)
	s, ok := f.files[id]
	if !ok || int64(len(s)) > max {
		return nil, fmt.Errorf("no asset %d", id)
	}
	return []byte(s), nil
}

const goodSha = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func infoJSON(id, ver, name string, size int) string {
	return fmt.Sprintf(`{"id":%q,"version":%q,"sha256":%q,"size":%d,"meta":{"id":%q,"name":%q,"version":%q,"description":"Plays\nmusic","permissions":["storage","chat.send"]}}`,
		id, ver, goodSha, size, id, name, ver)
}

func wasmRelease(id, ver string, idBase int64, withInfo bool) Release {
	r := Release{Tag: id + "-v" + ver, Assets: []ReleaseAsset{{ID: idBase + 1, Name: id + ".wasm", Size: 1000}, {ID: idBase + 2, Name: id + ".wasm.sha256", Size: 80}}}
	if withInfo {
		r.Assets = append(r.Assets, ReleaseAsset{ID: idBase + 3, Name: id + ".json", Size: 300})
	}
	return r
}

func newWasmMarket(t *testing.T, f *fakeReleases) *Market {
	t.Helper()
	m, err := New(Config{Dir: t.TempDir(), Releases: f})
	if err != nil {
		t.Fatal(err)
	}
	m.st.Sources = []string{"o/n"} // без файла состояния официальный источник не добавляется
	return m
}

func TestWasmCatalogGroupsReleasesAndReadsInfo(t *testing.T) {
	f := &fakeReleases{
		list: []Release{
			wasmRelease("music", "0.9.0", 100, true),
			wasmRelease("music", "0.10.0", 150, true), // 0.10.0 новее 0.9.0 (не строковое сравнение)
			wasmRelease("music", "0.8.1", 200, false),
			{Tag: "music-v1.0.0-rc1"}, {Tag: "v1.2.3"}, {Tag: "docs"}, // не наши теги
			{Tag: "music-v2.0.0", Prerelease: true, Assets: wasmRelease("music", "2.0.0", 300, true).Assets}, // предрелиз
			{Tag: "music-v3.0.0", Draft: true},
			wasmRelease("osu-tools", "1.0.0", 400, false),
		},
		files: map[int64]string{153: infoJSON("music", "0.10.0", "Music", 1000)},
	}
	m := newWasmMarket(t, f)
	cat := m.WasmCatalog(context.Background(), false)
	if len(cat) != 1 || cat[0].Error != "" || len(cat[0].Plugins) != 2 {
		t.Fatalf("%+v", cat)
	}
	music, osu := cat[0].Plugins[0], cat[0].Plugins[1]
	if music.ID != "music" || music.Version != "0.10.0" || music.Tag != "music-v0.10.0" || music.NoInfo {
		t.Fatalf("music: %+v", music)
	}
	if music.Name != "Music" || music.Description != "Plays music" || music.Sha256 != goodSha || music.Size != 1000 || strings.Join(music.Permissions, ",") != "storage,chat.send" {
		t.Fatalf("music info: %+v", music)
	}
	var vs []string
	for _, v := range music.Versions {
		vs = append(vs, v.Version)
	}
	if strings.Join(vs, " ") != "0.10.0 0.9.0 0.8.1" {
		t.Fatalf("версии: %v", vs)
	}
	if osu.ID != "osu-tools" || !osu.NoInfo || osu.Name != "osu-tools" || osu.Sha256 != "" {
		t.Fatalf("osu без json: %+v", osu)
	}
	// кэш: второй вызов не ходит в сеть, refresh ходит
	m.WasmCatalog(context.Background(), false)
	if f.listed.Load() != 1 {
		t.Fatalf("кэш не сработал: %d", f.listed.Load())
	}
	m.WasmCatalog(context.Background(), true)
	if f.listed.Load() != 2 {
		t.Fatalf("refresh: %d", f.listed.Load())
	}
}

func TestWasmCatalogDistrustsMismatchingInfo(t *testing.T) {
	bad := map[string]string{
		"чужой id":        infoJSON("other", "1.0.0", "X", 1000),
		"другая версия":   infoJSON("music", "1.0.1", "X", 1000),
		"размер не тот":   infoJSON("music", "1.0.0", "X", 999),
		"не JSON":         "nope",
		"плохой sha256":   strings.Replace(infoJSON("music", "1.0.0", "X", 1000), goodSha, "ZZ", 1),
		"слишком большой": infoJSON("music", "1.0.0", "X", 100<<20),
	}
	for name, js := range bad {
		f := &fakeReleases{list: []Release{wasmRelease("music", "1.0.0", 0, true)}, files: map[int64]string{3: js}}
		e := newWasmMarket(t, f).WasmCatalog(context.Background(), true)[0].Plugins[0]
		if !e.NoInfo || e.Name != "music" || e.Sha256 != "" || len(e.Permissions) != 0 {
			t.Errorf("%s: метаданным нельзя верить: %+v", name, e)
		}
	}
}

func TestWasmCatalogSanitizesText(t *testing.T) {
	js := `{"id":"music","version":"1.0.0","sha256":"` + goodSha + `","size":1000,"meta":{"id":"music","version":"1.0.0","name":"  A\u0000B\nC  ","author":"` + strings.Repeat("я", 200) + `","permissions":["` + strings.Repeat("p", 50) + `","ok"]}}`
	f := &fakeReleases{list: []Release{wasmRelease("music", "1.0.0", 0, true)}, files: map[int64]string{3: js}}
	e := newWasmMarket(t, f).WasmCatalog(context.Background(), true)[0].Plugins[0]
	if e.Name != "A B C" || len([]rune(e.Author)) != 80 || len(e.Permissions) != 1 || e.Permissions[0] != "ok" {
		t.Fatalf("%q %d %v", e.Name, len([]rune(e.Author)), e.Permissions)
	}
}

func TestWasmCatalogSourceErrorAndEmpty(t *testing.T) {
	f := &fakeReleases{err: fmt.Errorf("repository not found")}
	cat := newWasmMarket(t, f).WasmCatalog(context.Background(), true)
	if cat[0].Error == "" || len(cat[0].Plugins) != 0 || cat[0].Plugins == nil {
		t.Fatalf("%+v", cat[0])
	}
	f = &fakeReleases{list: []Release{{Tag: "other"}}}
	cat = newWasmMarket(t, f).WasmCatalog(context.Background(), true)
	if cat[0].Error != "" || cat[0].Plugins == nil || len(cat[0].Plugins) != 0 {
		t.Fatalf("пустой источник — не ошибка: %+v", cat[0])
	}
}

func TestHTTPReleasesListAndAsset(t *testing.T) {
	var auth []string
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = append(auth, r.URL.Path+" "+r.Header.Get("Authorization"))
		switch {
		case r.URL.Path == "/repos/o/n/releases":
			fmt.Fprint(w, `[{"tag_name":"music-v1.0.0","draft":false,"prerelease":false,"assets":[{"id":7,"name":"music.json","size":12}]}]`)
		case r.URL.Path == "/repos/o/n/releases/assets/7":
			if !strings.Contains(r.Header.Get("Accept"), "octet-stream") {
				http.Error(w, "accept", 400)
				return
			}
			http.Redirect(w, r, srv.URL+"/signed/7", http.StatusFound)
		case r.URL.Path == "/signed/7":
			fmt.Fprint(w, "hello")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	h := HTTPReleases(srv.URL)
	repo := Repo{Owner: "o", Name: "n"}
	list, err := h.List(context.Background(), repo, "tok")
	if err != nil || len(list) != 1 || list[0].Tag != "music-v1.0.0" || list[0].Assets[0].ID != 7 {
		t.Fatalf("%+v %v", list, err)
	}
	b, err := h.Asset(context.Background(), repo, "tok", 7, 100)
	if err != nil || string(b) != "hello" {
		t.Fatalf("%q %v", b, err)
	}
	if _, err := h.Asset(context.Background(), repo, "", 7, 3); err == nil {
		t.Fatal("лимит размера должен срабатывать")
	}
	if _, err := h.List(context.Background(), Repo{Owner: "o", Name: "missing"}, ""); err == nil || strings.Contains(err.Error(), "Bearer") {
		t.Fatalf("404: %v", err)
	}
	if !strings.Contains(auth[0], "Bearer tok") {
		t.Fatalf("токен не передан API: %v", auth)
	}
}

func TestMarkWasmDeprecatedSortsLast(t *testing.T) {
	list := []WasmEntry{{ID: "osu-skills"}, {ID: "music"}, {ID: "osu-tools"}}
	markWasmDeprecated(list, []Deprecation{
		{ID: "osu-skills", Kind: KindWasm, Reason: "merged"},
		{ID: "music", Kind: KindWidget, Reason: "widget with the same id is not the wasm plugin"},
	})
	var ids []string
	for _, e := range list {
		ids = append(ids, e.ID)
	}
	if got := strings.Join(ids, ","); got != "music,osu-tools,osu-skills" {
		t.Fatalf("порядок %s", got)
	}
	if list[2].Deprecated == nil || list[2].Deprecated.Reason != "merged" || list[0].Deprecated != nil || list[1].Deprecated != nil {
		t.Fatalf("пометки: %+v", list)
	}
}
