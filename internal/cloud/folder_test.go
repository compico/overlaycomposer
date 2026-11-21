package cloud

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func connected(t *testing.T) (*Folder, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "sync")
	f := NewFolder(dir)
	if err := f.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	return f, dir
}

func TestFolderNeedsConnectAndAbsolutePath(t *testing.T) {
	ctx := context.Background()
	f := NewFolder(t.TempDir())
	if _, err := f.List(ctx); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("List до Connect: %v", err)
	}
	if err := NewFolder("relative/dir").Connect(ctx); err == nil {
		t.Fatal("относительный путь принят")
	}
	if err := f.Connect(ctx); err != nil || !f.Connected() {
		t.Fatalf("Connect: %v", err)
	}
	_ = f.Disconnect()
	if f.Connected() {
		t.Fatal("после Disconnect всё ещё подключён")
	}
}

func TestFolderPutGetListDelete(t *testing.T) {
	ctx := context.Background()
	f, dir := connected(t)
	r, err := f.Put(ctx, "oc-settings-pc-1.json", []byte(`{"a":1}`), "")
	if err != nil || r.Fingerprint != Fingerprint([]byte(`{"a":1}`)) || r.Size != 7 {
		t.Fatalf("Put: %+v %v", r, err)
	}
	// Чужие файлы и временные остатки в список не попадают.
	_ = os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o600)
	_ = os.WriteFile(filepath.Join(dir, ".oc-put-leftover"), []byte("x"), 0o600)
	list, err := f.List(ctx)
	if err != nil || len(list) != 1 || list[0].ID != "oc-settings-pc-1.json" {
		t.Fatalf("List: %+v %v", list, err)
	}
	got, err := f.Get(ctx, "oc-settings-pc-1.json")
	if err != nil || string(got) != `{"a":1}` {
		t.Fatalf("Get: %q %v", got, err)
	}
	if err := f.Delete(ctx, "oc-settings-pc-1.json"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Get(ctx, "oc-settings-pc-1.json"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get после Delete: %v", err)
	}
	if err := f.Delete(ctx, "oc-settings-pc-1.json"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("повторный Delete: %v", err)
	}
}

func TestFolderPutNeverOverwritesForeignVersion(t *testing.T) {
	ctx := context.Background()
	f, _ := connected(t)
	const name = "oc-settings-pc.json"
	first, _ := f.Put(ctx, name, []byte("one"), "")
	if _, err := f.Put(ctx, name, []byte("two"), ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("перезапись без expect: %v", err)
	}
	if _, err := f.Put(ctx, name, []byte("two"), Fingerprint([]byte("other"))); !errors.Is(err, ErrConflict) {
		t.Fatalf("перезапись с чужим отпечатком: %v", err)
	}
	if _, err := f.Put(ctx, "oc-settings-new.json", []byte("x"), first.Fingerprint); !errors.Is(err, ErrConflict) {
		t.Fatalf("expect для несуществующего файла: %v", err)
	}
	if got, _ := f.Get(ctx, name); string(got) != "one" {
		t.Fatalf("файл изменился при конфликте: %q", got)
	}
	if _, err := f.Put(ctx, name, []byte("two"), first.Fingerprint); err != nil {
		t.Fatalf("замена известной версии: %v", err)
	}
	if got, _ := f.Get(ctx, name); string(got) != "two" {
		t.Fatalf("после замены: %q", got)
	}
}

func TestValidNameRejectsPaths(t *testing.T) {
	f, _ := connected(t)
	for _, n := range []string{"../oc-settings-x.json", "oc-settings-/x.json", "oc-settings-x.txt", "settings.json", "oc-settings-.json", `oc-settings-a\b.json`} {
		if ValidName(n) == nil {
			t.Errorf("%q принято", n)
		}
		if _, err := f.Get(context.Background(), n); err == nil || errors.Is(err, ErrNotFound) {
			t.Errorf("Get(%q): %v", n, err)
		}
	}
	if err := ValidName("oc-settings-DESKTOP-1-20261121.json"); err != nil {
		t.Fatal(err)
	}
}

func TestFolderRejectsOversizedPut(t *testing.T) {
	f, _ := connected(t)
	if _, err := f.Put(context.Background(), "oc-settings-big.json", make([]byte, MaxFileSize+1), ""); err == nil {
		t.Fatal("большой файл принят")
	}
}
