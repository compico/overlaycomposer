package kv

import (
	"encoding/json"
	"strings"
	"testing"
)

func raw(s string) json.RawMessage { return json.RawMessage(s) }
func i64(v int64) *int64           { return &v }

func TestSetGetRevisions(t *testing.T) {
	s := New(NewMemory())
	var changes []Change
	s.OnChange = func(c Change) { changes = append(changes, c) }

	if _, found, err := s.Get("p", "tasks"); err != nil || found {
		t.Fatalf("missing key: %v %v", found, err)
	}
	rev, err := s.Set("p", "tasks", raw(`{ "a": [1, 2] }`), nil)
	if err != nil || rev != 1 {
		t.Fatalf("set: %d %v", rev, err)
	}
	e, found, _ := s.Get("p", "tasks")
	if !found || string(e.Value) != `{"a":[1,2]}` || e.Rev != 1 {
		t.Fatalf("get: %+v", e)
	}
	if rev, _ = s.Set("p", "tasks", raw(`"x"`), nil); rev != 2 {
		t.Fatalf("rev: %d", rev)
	}
	// пространства имён плагинов не пересекаются
	if _, found, _ := s.Get("other", "tasks"); found {
		t.Fatal("namespaces leak")
	}
	if len(changes) != 2 || changes[0].Plugin != "p" || changes[1].Rev != 2 || string(changes[1].Value) != `"x"` {
		t.Fatalf("changes: %+v", changes)
	}
}

func TestCompareAndSet(t *testing.T) {
	s := New(NewMemory())
	// ifRev=0: «ключа ещё нет»
	if _, err := s.Set("p", "k", raw(`1`), i64(0)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Set("p", "k", raw(`2`), i64(0)); err != ErrConflict {
		t.Fatalf("second create: %v", err)
	}
	if _, err := s.Set("p", "k", raw(`2`), i64(5)); err != ErrConflict {
		t.Fatalf("stale rev: %v", err)
	}
	if rev, err := s.Set("p", "k", raw(`2`), i64(1)); err != nil || rev != 2 {
		t.Fatalf("matching rev: %d %v", rev, err)
	}
	if e, _, _ := s.Get("p", "k"); string(e.Value) != "2" {
		t.Fatalf("conflict must not change value: %s", e.Value)
	}
}

func TestDeleteListClear(t *testing.T) {
	s := New(NewMemory())
	var changes []Change
	s.OnChange = func(c Change) { changes = append(changes, c) }
	for _, k := range []string{"task:2", "task:1", "cfg", "task:3"} {
		if _, err := s.Set("p", k, raw(`true`), nil); err != nil {
			t.Fatal(err)
		}
	}
	l, err := s.List("p", "task:", 0)
	if err != nil || len(l) != 3 || l[0].Key != "task:1" || l[2].Key != "task:3" {
		t.Fatalf("list: %+v %v", l, err)
	}
	if l, _ := s.List("p", "", 2); len(l) != 2 {
		t.Fatalf("limit: %d", len(l))
	}
	if ok, _ := s.Delete("p", "cfg"); !ok {
		t.Fatal("delete existing")
	}
	if ok, _ := s.Delete("p", "cfg"); ok {
		t.Fatal("delete missing reported as deleted")
	}
	changes = nil
	if err := s.Clear("p"); err != nil {
		t.Fatal(err)
	}
	if st, _ := s.Stats("p"); st.Keys != 0 || st.Bytes != 0 {
		t.Fatalf("stats after clear: %+v", st)
	}
	if len(changes) != 3 || !changes[0].Deleted {
		t.Fatalf("clear notifications: %+v", changes)
	}
}

func TestValidationAndQuotas(t *testing.T) {
	s := New(NewMemory())
	for _, k := range []string{"", "has space", "ключ", strings.Repeat("a", 129), "a\nb"} {
		if _, err := s.Set("p", k, raw(`1`), nil); err != ErrBadKey {
			t.Errorf("key %q: %v", k, err)
		}
	}
	for _, v := range []string{``, `{bad`, `undefined`, strings.Repeat("1", MaxValueLen+1)} {
		if _, err := s.Set("p", "k", raw(v), nil); err != ErrBadValue {
			t.Errorf("value %.10q: %v", v, err)
		}
	}
	if _, err := s.Set("bad plugin!", "k", raw(`1`), nil); err != ErrBadPlug {
		t.Errorf("plugin id: %v", err)
	}
	// общий размер: значения по ~200 КБ, пока не упрёмся в MaxBytes
	big := raw(`"` + strings.Repeat("x", 200<<10) + `"`)
	var n int
	var err error
	for ; err == nil && n < 100; n++ {
		_, err = s.Set("p", "big"+string(rune('a'+n%26))+string(rune('a'+n/26)), big, nil)
	}
	if err != ErrQuota || n < 30 {
		t.Fatalf("byte quota: n=%d err=%v", n, err)
	}
	// перезапись существующего ключа не увеличивает размер сверх квоты
	if _, err := s.Set("p", "baaa", raw(`1`), nil); err != nil {
		t.Fatalf("shrinking overwrite must succeed: %v", err)
	}
	// ключи
	s2 := New(NewMemory())
	for i := 0; i < MaxKeys; i++ {
		if _, err := s2.Set("p", "k"+itoa(i), raw(`1`), nil); err != nil {
			t.Fatalf("key %d: %v", i, err)
		}
	}
	if _, err := s2.Set("p", "overflow", raw(`1`), nil); err != ErrQuota {
		t.Fatalf("key quota: %v", err)
	}
	if _, err := s2.Set("p", "k1", raw(`2`), nil); err != nil {
		t.Fatalf("overwrite at the key limit: %v", err)
	}
}

func itoa(i int) string {
	b, _ := json.Marshal(i)
	return string(b)
}
