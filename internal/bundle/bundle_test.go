package bundle

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// memSection — секция-словарь id → значение: имитирует хранилище.
type memSection struct {
	name    string
	data    map[string]string
	applied []string
	failOn  string
}

func (m *memSection) Name() string { return m.name }
func (m *memSection) Export(context.Context) (json.RawMessage, error) {
	return json.Marshal(m.data)
}
func (m *memSection) Plan(_ context.Context, raw json.RawMessage) ([]Item, error) {
	var in map[string]string
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, err
	}
	var out []Item
	for _, id := range []string{"a", "b", "c", "d"} {
		v, ok := in[id]
		if !ok {
			continue
		}
		it := Item{ID: id, Name: id}
		switch cur, have := m.data[id]; {
		case v == "bad":
			it.Status, it.Reason = StatusProblem, "broken"
		case !have:
			it.Status = StatusNew
		case cur == v:
			it.Status = StatusSame
		default:
			it.Status = StatusReplace
		}
		out = append(out, it)
	}
	return out, nil
}
func (m *memSection) Apply(_ context.Context, raw json.RawMessage, pick map[string]string) ([]Outcome, error) {
	var in map[string]string
	_ = json.Unmarshal(raw, &in)
	var out []Outcome
	for _, id := range []string{"a", "b", "c", "d"} {
		if _, ok := pick[id]; !ok {
			continue
		}
		if id == m.failOn {
			out = append(out, Outcome{ID: id, Error: "boom"})
			continue
		}
		m.data[id] = in[id]
		m.applied = append(m.applied, id)
		out = append(out, Outcome{ID: id, OK: true})
	}
	return out, nil
}

func newSvc(a, b *memSection) *Service {
	s := New("test", a, b)
	s.Now = func() time.Time { return time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC) }
	return s
}

func roundtrip(t *testing.T, s *Service, names ...string) File {
	t.Helper()
	f, err := s.Export(context.Background(), names)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.MarshalIndent(f, "", "  ") // пользователь мог переформатировать файл
	got, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestExportParseAndSelectSections(t *testing.T) {
	a := &memSection{name: "layouts", data: map[string]string{"a": "1"}}
	b := &memSection{name: "deck", data: map[string]string{"a": "2"}}
	s := newSvc(a, b)
	if f := roundtrip(t, s); len(f.Sections) != 2 || f.Format != Format || f.CreatedAt != "2026-11-20T09:00:00Z" {
		t.Fatalf("%+v", f)
	}
	if f := roundtrip(t, s, "deck"); len(f.Sections) != 1 || f.Sections["deck"] == nil {
		t.Fatalf("%+v", f)
	}
	if _, err := s.Export(context.Background(), []string{"nope"}); err == nil {
		t.Fatal("неизвестная секция")
	}
}

func TestParseRejects(t *testing.T) {
	a := &memSection{name: "layouts", data: map[string]string{"a": "1"}}
	f, _ := newSvc(a, &memSection{name: "deck"}).Export(context.Background(), []string{"layouts"})
	good, _ := json.Marshal(f)
	cases := map[string]struct {
		data string
		want error
	}{
		"не json":         {"{", ErrNotBundle},
		"не бандл":        {`{"hello":1}`, ErrNotBundle},
		"новее":           {`{"format":99,"sections":{},"checksums":{}}`, ErrTooNew},
		"правка секции":   {strings.Replace(string(good), `"a":"1"`, `"a":"2"`, 1), ErrCorrupt},
		"нет контрольной": {strings.Replace(string(good), `"checksums":{"layouts"`, `"checksums":{"other"`, 1), ErrCorrupt},
	}
	for name, c := range cases {
		if _, err := Parse([]byte(c.data)); !errors.Is(err, c.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := Parse(good); err != nil {
		t.Fatal(err)
	}
}

func TestPlanAndUnknownSections(t *testing.T) {
	src := newSvc(&memSection{name: "layouts", data: map[string]string{"a": "1", "b": "bad", "c": "3"}}, &memSection{name: "future"})
	f := roundtrip(t, src)
	dst := newSvc(&memSection{name: "layouts", data: map[string]string{"a": "1", "c": "other"}}, &memSection{name: "deck", data: map[string]string{}})
	plan := dst.Plan(context.Background(), f)
	if len(plan) != 2 || plan[0].Name != "layouts" || !plan[0].Known || plan[1].Name != "future" || plan[1].Known {
		t.Fatalf("%+v", plan)
	}
	st := map[string]string{}
	for _, it := range plan[0].Items {
		st[it.ID] = it.Status
	}
	if st["a"] != StatusSame || st["b"] != StatusProblem || st["c"] != StatusReplace {
		t.Fatalf("%v", st)
	}
}

func TestApplyOrderBackupAndErrors(t *testing.T) {
	srcA := &memSection{name: "layouts", data: map[string]string{"a": "1", "b": "bad", "c": "3"}}
	srcB := &memSection{name: "deck", data: map[string]string{"a": "x"}}
	f := roundtrip(t, newSvc(srcA, srcB))
	a := &memSection{name: "layouts", data: map[string]string{"a": "old"}, failOn: "c"}
	b := &memSection{name: "deck", data: map[string]string{}}
	dst := newSvc(a, b)
	backups := 0
	dst.BeforeApply = func(context.Context) error { backups++; return nil }
	ctx := context.Background()

	// проблемный элемент выбрать нельзя
	if _, err := dst.Apply(ctx, f, Selection{"layouts": {"b": ModeReplace}}); err == nil || !strings.Contains(err.Error(), "cannot be imported") || backups != 0 {
		t.Fatalf("%v %d", err, backups)
	}
	// элемента нет в файле, неизвестные секция и режим
	for _, sel := range []Selection{{"layouts": {"zzz": ModeReplace}}, {"nope": {"a": ModeReplace}}, {"layouts": {"a": "merge"}}} {
		if _, err := dst.Apply(ctx, f, sel); err == nil {
			t.Fatalf("%v", sel)
		}
	}
	if backups != 0 || len(a.applied) != 0 {
		t.Fatal("ничего не должно примениться")
	}
	// пустой выбор — не трогаем и не делаем бэкап
	if r, err := dst.Apply(ctx, f, Selection{}); err != nil || len(r) != 0 || backups != 0 {
		t.Fatalf("%v %v %d", r, err, backups)
	}
	// нормальное применение: бэкап один раз, порядок секций как в сервисе, ошибка элемента не мешает остальным
	r, err := dst.Apply(ctx, f, Selection{"deck": {"a": ModeReplace}, "layouts": {"a": ModeReplace, "c": ModeReplace}})
	if err != nil || backups != 1 || len(r) != 2 || r[0].Name != "layouts" || r[1].Name != "deck" {
		t.Fatalf("%v %v %d", r, err, backups)
	}
	if a.data["a"] != "1" || a.data["c"] != "" || b.data["a"] != "x" {
		t.Fatalf("%v %v", a.data, b.data)
	}
	if r[0].Outcomes[1].OK || r[0].Outcomes[1].Error != "boom" || !r[0].Outcomes[0].OK {
		t.Fatalf("%+v", r[0].Outcomes)
	}
}

func TestBackupFailureCancelsApply(t *testing.T) {
	src := &memSection{name: "layouts", data: map[string]string{"a": "1"}}
	f := roundtrip(t, newSvc(src, &memSection{name: "deck"}))
	dstA := &memSection{name: "layouts", data: map[string]string{}}
	dst := newSvc(dstA, &memSection{name: "deck", data: map[string]string{}})
	dst.BeforeApply = func(context.Context) error { return errors.New("disk full") }
	if _, err := dst.Apply(context.Background(), f, Selection{"layouts": {"a": ModeReplace}}); err == nil || !strings.Contains(err.Error(), "nothing was changed") {
		t.Fatal(err)
	}
	if len(dstA.data) != 0 {
		t.Fatal("применилось без бэкапа")
	}
}

func TestWriteBackupKeepsLast(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "backups")
	f := File{Format: Format, Sections: map[string]json.RawMessage{}, Checksums: map[string]string{}}
	base := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	var last string
	for i := 0; i < 5; i++ {
		p, err := WriteBackup(dir, f, base.Add(time.Duration(i)*time.Second), 3, KindManual)
		if err != nil {
			t.Fatal(err)
		}
		last = p
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 3 {
		t.Fatalf("%d файлов", len(entries))
	}
	if _, err := os.Stat(last); err != nil {
		t.Fatal("последний бэкап удалён")
	}
	data, _ := os.ReadFile(last)
	if _, err := Parse(data); err != nil {
		t.Fatalf("бэкап читается как бандл: %v", err)
	}
}

func TestBackupListReadDeleteAndKinds(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "backups")
	if list, err := ListBackups(dir); err != nil || len(list) != 0 {
		t.Fatalf("нет каталога: %v %v", list, err)
	}
	f := File{Format: Format, Sections: map[string]json.RawMessage{}, Checksums: map[string]string{}}
	base := time.Date(2026, 11, 21, 19, 0, 0, 0, time.UTC)
	for i, k := range []string{KindAuto, KindManual, KindImport} {
		if _, err := WriteBackup(dir, f, base.Add(time.Duration(i)*time.Minute), 0, k); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := WriteBackup(dir, f, base, 0, "weird"); err == nil {
		t.Fatal("неизвестный вид принят")
	}
	// ранний бэкап без вида в имени — это бэкап перед импортом
	_ = os.WriteFile(filepath.Join(dir, "oc-settings-20260101-000000.000.json"), []byte("{}"), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o600)
	list, err := ListBackups(dir)
	if err != nil || len(list) != 4 {
		t.Fatalf("%+v %v", list, err)
	}
	if list[0].Kind != KindImport || list[1].Kind != KindManual || list[2].Kind != KindAuto || list[3].Kind != KindImport {
		t.Fatalf("порядок или виды: %+v", list)
	}
	if !list[0].ModTime.Equal(base.Add(2 * time.Minute)) {
		t.Fatalf("время из имени: %v", list[0].ModTime)
	}
	if data, err := ReadBackup(dir, list[1].Name); err != nil || len(data) == 0 {
		t.Fatalf("ReadBackup: %v", err)
	}
	for _, bad := range []string{"../x.json", "notes.txt", "oc-settings-x.json", "oc-settings-20260101-000000.000-evil.json"} {
		if _, err := ReadBackup(dir, bad); err == nil {
			t.Errorf("ReadBackup(%q) принят", bad)
		}
		if err := DeleteBackup(dir, bad); err == nil {
			t.Errorf("DeleteBackup(%q) принят", bad)
		}
	}
	if err := DeleteBackup(dir, list[0].Name); err != nil {
		t.Fatal(err)
	}
	if list, _ := ListBackups(dir); len(list) != 3 {
		t.Fatalf("после удаления: %d", len(list))
	}
}

func TestAutosaveOnlyWhenChanged(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "backups")
	mk := func(sum string) File {
		return File{Format: Format, CreatedAt: time.Now().String(), Sections: map[string]json.RawMessage{}, Checksums: map[string]string{"layouts": sum, "deck": "d"}}
	}
	now := time.Date(2026, 11, 21, 19, 0, 0, 0, time.UTC)
	sig1, wrote, err := Autosave(dir, mk("a"), "", now, 30)
	if err != nil || !wrote {
		t.Fatalf("первый снимок: %v %v", wrote, err)
	}
	if _, wrote, _ := Autosave(dir, mk("a"), sig1, now.Add(time.Minute), 30); wrote {
		t.Fatal("снимок без изменений")
	}
	sig2, wrote, _ := Autosave(dir, mk("b"), sig1, now.Add(2*time.Minute), 30)
	if !wrote || sig2 == sig1 {
		t.Fatal("изменение не замечено")
	}
	if list, _ := ListBackups(dir); len(list) != 2 {
		t.Fatalf("файлов: %d", len(list))
	}
	if Signature(mk("a")) != Signature(File{Checksums: map[string]string{"deck": "d", "layouts": "a"}}) {
		t.Fatal("отпечаток зависит от порядка или времени создания")
	}
}
