package market

import (
	"context"
	"strings"
	"testing"
)

const depJSON = `[
 {"id":"old","kind":"widget","since":"2026-11-17","reason":"  Заменён.  ","replacedBy":{"id":"new","kind":"wasm","repo":"bob/tools","url":"https://example.com/releases"}},
 {"id":"oldwasm","kind":"wasm","reason":"Объединён.","replacedBy":{"id":"new","kind":"wasm","url":"javascript:alert(1)"}},
 {"id":"bad id","kind":"widget","reason":"x"},
 {"id":"nokind","kind":"plugin","reason":"x"},
 {"id":"noreason","kind":"widget","reason":" "},
 {"id":"old","kind":"widget","reason":"дубль"},
 {"id":"gone","kind":"widget","since":"вчера","reason":"Удалён.","replacedBy":{"id":"???","kind":"wasm"}}
]`

func depRepo(t *testing.T, dep string) []byte {
	return makeZip(t, append(plug("old", "1.0.0"), file{name: "repo-abc/deprecated.json", body: dep})...)
}

func TestParseDeprecatedFiltersAndCleans(t *testing.T) {
	got := parseDeprecatedZip(depRepo(t, depJSON), "Bob/tools")
	if len(got) != 3 {
		t.Fatalf("записей %d: %+v", len(got), got)
	}
	old := got[0]
	if old.ID != "old" || old.Reason != "Заменён." || old.Since != "2026-11-17" || old.Repo != "Bob/tools" {
		t.Fatalf("old: %+v", old)
	}
	if r := old.ReplacedBy; r == nil || r.ID != "new" || r.Kind != KindWasm || r.Repo != "bob/tools" || r.URL != "https://example.com/releases" {
		t.Fatalf("replacedBy: %+v", old.ReplacedBy)
	}
	// repo замены по умолчанию — источник; небезопасная ссылка отбрасывается
	if r := got[1].ReplacedBy; r == nil || r.Repo != "Bob/tools" || r.URL != "" {
		t.Fatalf("wasm replacement: %+v", got[1].ReplacedBy)
	}
	// кривая дата отбрасывается, кривая замена — тоже, но запись остаётся
	if got[2].ID != "gone" || got[2].Since != "" || got[2].ReplacedBy != nil {
		t.Fatalf("gone: %+v", got[2])
	}
	for _, bad := range []string{"{", "{}", `"x"`, "[1]", ""} {
		if l := parseDeprecatedZip(depRepo(t, bad), "a/b"); len(l) != 0 {
			t.Errorf("%q: %+v", bad, l)
		}
	}
	if l := parseDeprecatedZip(makeZip(t, plug("old", "1")...), "a/b"); l != nil {
		t.Fatalf("без файла: %+v", l)
	}
	// файл внутри plugins/<id>/ — не deprecated.json репозитория
	nested := makeZip(t, file{name: "repo-abc/plugins/x/deprecated.json", body: depJSON})
	if l := parseDeprecatedZip(nested, "a/b"); l != nil {
		t.Fatalf("вложенный: %+v", l)
	}
	huge := `[{"id":"a","kind":"widget","reason":"` + strings.Repeat("я", maxDeprecatedSize) + `"}]`
	if l := parseDeprecatedZip(depRepo(t, huge), "a/b"); l != nil {
		t.Fatal("слишком большой файл должен игнорироваться")
	}
}

func TestCatalogCarriesDeprecations(t *testing.T) {
	e := newEnv(t)
	e.repos["bob/tools"] = depRepo(t, depJSON)
	if _, err := e.m.AddSource(context.Background(), "bob/tools"); err != nil {
		t.Fatal(err)
	}
	cat := e.m.Catalog(context.Background(), true)
	sv := cat[len(cat)-1]
	if len(sv.Deprecated) != 3 || sv.Plugins[0].Deprecated == nil || sv.Plugins[0].Deprecated.ID != "old" {
		t.Fatalf("%+v", sv)
	}
	// источник без deprecated.json отдаёт пустой список, а не null
	e.repos["bob/plain"] = makeZip(t, plug("alpha", "1")...)
	e.m.AddSource(context.Background(), "bob/plain")
	for _, s := range e.m.Catalog(context.Background(), true) {
		if s.Repo == "bob/plain" && (s.Deprecated == nil || len(s.Deprecated) != 0) {
			t.Fatalf("plain: %#v", s.Deprecated)
		}
	}
}

func TestDeprecatedInstalled(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	e.repos["bob/tools"] = depRepo(t, depJSON)
	if _, err := e.m.AddSource(ctx, "bob/tools"); err != nil {
		t.Fatal(err)
	}
	// ничего не установлено
	if l, ok := e.m.DeprecatedInstalled(ctx, true, nil); !ok || len(l) != 0 {
		t.Fatalf("пусто: %v %+v", ok, l)
	}
	if _, err := e.m.Install(ctx, "bob/tools", "old", ""); err != nil {
		t.Fatal(err)
	}
	l, ok := e.m.DeprecatedInstalled(ctx, false, map[string]string{"oldwasm": "Old wasm", "unrelated": "Other"})
	if !ok || len(l) != 2 {
		t.Fatalf("%v %+v", ok, l)
	}
	if l[0].ID != "old" || l[0].Name != "Plugin old" || l[1].ID != "oldwasm" || l[1].Name != "Old wasm" || l[1].Kind != KindWasm {
		t.Fatalf("%+v", l)
	}
	// «gone» не установлен ни как виджет, ни как wasm — не попадает
	// плагин, установленный из другого репозитория, объявлением чужого источника не помечается
	e.repos["eve/evil"] = depRepo(t, depJSON)
	if _, err := e.m.AddSource(ctx, "eve/evil"); err != nil {
		t.Fatal(err)
	}
	l, _ = e.m.DeprecatedInstalled(ctx, false, nil)
	if len(l) != 1 || l[0].Repo != "bob/tools" {
		t.Fatalf("%+v", l)
	}
	// все источники недоступны — ok=false
	e2 := newEnv(t)
	if _, ok := e2.m.DeprecatedInstalled(ctx, true, nil); ok {
		t.Fatal("без ответивших источников ok=false")
	}
}
