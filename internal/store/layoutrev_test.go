package store

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/compico/overlaycomposer/internal/model"
)

func openTemp(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "oc.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func newLayout(id, name string) model.Layout {
	return model.Layout{ID: id, Name: name, Scenes: []string{}, Canvas: model.Canvas{Width: 1920, Height: 1080}, Objects: []model.Object{}}
}

func ptr(v int64) *int64 { return &v }

func TestLayoutRevIncrementsOnEverySave(t *testing.T) {
	s := openTemp(t)
	rev, _, err := s.SaveLayoutRev(newLayout("a", "A"), nil)
	if err != nil || rev != 1 {
		t.Fatalf("создание: rev=%d err=%v", rev, err)
	}
	l, _ := s.GetLayout("a")
	if l.Rev != 1 {
		t.Fatalf("GetLayout.Rev = %d", l.Rev)
	}
	rev, _, err = s.SaveLayoutRev(*l, ptr(1))
	if err != nil || rev != 2 {
		t.Fatalf("сохранение с актуальным rev: rev=%d err=%v", rev, err)
	}
	if _, err := s.SaveLayout(*l); err != nil { // безусловное (серверная правка) тоже растит версию
		t.Fatal(err)
	}
	if l2, _ := s.GetLayout("a"); l2.Rev != 3 {
		t.Fatalf("после безусловного сохранения rev = %d", l2.Rev)
	}
}

// sad path: две вкладки правят один layout — вторая получает конфликт и ничего не затирает.
func TestStaleLayoutRevIsRejectedAndNothingChanges(t *testing.T) {
	s := openTemp(t)
	_, _, _ = s.SaveLayoutRev(newLayout("a", "A"), nil) // rev 1
	tab1, _ := s.GetLayout("a")
	tab2, _ := s.GetLayout("a")

	tab1.Name = "From tab 1"
	if _, _, err := s.SaveLayoutRev(*tab1, ptr(tab1.Rev)); err != nil {
		t.Fatal(err)
	}
	tab2.Name = "From tab 2"
	_, _, err := s.SaveLayoutRev(*tab2, ptr(tab2.Rev))
	if !errors.Is(err, ErrRevConflict) {
		t.Fatalf("err = %v, want ErrRevConflict", err)
	}
	got, _ := s.GetLayout("a")
	if got.Name != "From tab 1" || got.Rev != 2 {
		t.Fatalf("конфликтное сохранение изменило данные: %+v", got)
	}
	// после перечитывания вторая вкладка может сохранить
	fresh, _ := s.GetLayout("a")
	fresh.Name = "From tab 2"
	if _, _, err := s.SaveLayoutRev(*fresh, ptr(fresh.Rev)); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteSourceBumpsRevOfAffectedLayoutsOnly(t *testing.T) {
	s := openTemp(t)
	if err := s.SaveSource(model.Source{ID: "src", Name: "S", Type: "iframe", Data: model.SourceData{URL: "https://x.example"}}); err != nil {
		t.Fatal(err)
	}
	with := newLayout("with", "With")
	with.Objects = []model.Object{{ID: "o1", SourceID: "src"}}
	_, _, _ = s.SaveLayoutRev(with, nil)
	_, _, _ = s.SaveLayoutRev(newLayout("without", "Without"), nil)

	if ok, err := s.DeleteSource("src"); err != nil || !ok {
		t.Fatalf("%v %v", ok, err)
	}
	a, _ := s.GetLayout("with")
	b, _ := s.GetLayout("without")
	if a.Rev != 2 || len(a.Objects) != 0 {
		t.Fatalf("затронутый layout: rev=%d objs=%d", a.Rev, len(a.Objects))
	}
	if b.Rev != 1 {
		t.Fatalf("незатронутый layout не должен менять rev: %d", b.Rev)
	}
}

// База версии 1 (без колонки rev) обновляется миграцией 2; старые layout получают rev 0.
func TestMigrationAddsRevToExistingLayouts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oc.db")
	db := rawDB(t, path)
	for _, q := range []string{
		"CREATE TABLE layouts (id text PRIMARY KEY, name text, canvas_w integer, canvas_h integer, objects text, updated_at datetime)",
		"CREATE TABLE layout_scenes (scene text PRIMARY KEY, layout_id text NOT NULL, pos integer)",
		"INSERT INTO layouts (id, name, canvas_w, canvas_h, objects) VALUES ('old', 'Old', 1920, 1080, '[]')",
		"PRAGMA user_version = 1",
	} {
		if err := db.Exec(q).Error; err != nil {
			t.Fatal(err)
		}
	}
	sqlDB, _ := db.DB()
	_ = sqlDB.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	l, err := s.GetLayout("old")
	if err != nil || l == nil || l.Rev != 0 || l.Name != "Old" {
		t.Fatalf("%+v %v", l, err)
	}
	if rev, _, err := s.SaveLayoutRev(*l, ptr(0)); err != nil || rev != 1 {
		t.Fatalf("rev=%d err=%v", rev, err)
	}
}
