package wasmplugins

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/compico/overlaycomposer/internal/kv"
)

func TestKVStorageIsolatesPlugins(t *testing.T) {
	s := KVStorage{kv.New(kv.NewMemory())}
	ctx := context.Background()
	if err := s.Set(ctx, "a", "k", json.RawMessage(`{"n":1}`)); err != nil {
		t.Fatal(err)
	}
	v, ok, err := s.Get(ctx, "a", "k")
	if err != nil || !ok || string(v) != `{"n":1}` {
		t.Fatalf("get: %s %v %v", v, ok, err)
	}
	if _, ok, _ := s.Get(ctx, "b", "k"); ok {
		t.Fatal("another plugin must not see the key")
	}
	if err := s.Delete(ctx, "a", "k"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Get(ctx, "a", "k"); ok {
		t.Fatal("deleted")
	}
	// sad path: плохой ключ и не-JSON значение
	if err := s.Set(ctx, "a", "bad key!", json.RawMessage(`1`)); err == nil {
		t.Fatal("bad key must fail")
	}
	if err := s.Set(ctx, "a", "k", json.RawMessage(`{oops`)); err == nil {
		t.Fatal("bad json must fail")
	}
}

func TestFileStatePersistsAndSurvivesBadWrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "wasmplugins.json")
	s, err := OpenFileState(path)
	if err != nil {
		t.Fatal(err)
	}
	if st, _ := s.Load("x"); st.Granted != nil || st.Config != nil {
		t.Fatal("empty state expected")
	}
	if err := s.Save("x", PluginState{Granted: []string{"storage"}, Config: json.RawMessage(`{"a":1}`)}); err != nil {
		t.Fatal(err)
	}
	s2, err := OpenFileState(path)
	if err != nil {
		t.Fatal(err)
	}
	st, _ := s2.Load("x")
	var cfg map[string]int // MarshalIndent переформатирует RawMessage: сравниваем по смыслу
	if err := json.Unmarshal(st.Config, &cfg); err != nil || len(st.Granted) != 1 || cfg["a"] != 1 {
		t.Fatalf("reload: %+v", st)
	}
	if ents, _ := os.ReadDir(filepath.Dir(path)); len(ents) != 1 {
		t.Fatalf("no temp files must remain: %v", ents)
	}
	// sad path: битый файл — ошибка, а не молчаливое затирание
	if err := os.WriteFile(path, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenFileState(path); err == nil {
		t.Fatal("a corrupted file must be reported")
	}
	// sad path: сохранить некуда — память не меняется
	blocker := filepath.Join(dir, "file")
	_ = os.WriteFile(blocker, nil, 0o600)
	bad, _ := OpenFileState(filepath.Join(blocker, "x.json"))
	if bad != nil {
		if err := bad.Save("y", PluginState{}); err == nil {
			t.Fatal("save into a path under a file must fail")
		}
		if st, _ := bad.Load("y"); st.Granted != nil {
			t.Fatal("failed save must not change the in-memory state")
		}
	}
}
