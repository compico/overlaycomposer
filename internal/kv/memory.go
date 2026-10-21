package kv

import (
	"sort"
	"strings"
	"sync"
)

// Memory — хранилище в памяти (для тестов).
type Memory struct {
	mu   sync.Mutex
	rows map[string]Row
}

func NewMemory() *Memory { return &Memory{rows: map[string]Row{}} }

type memTx struct{ m *Memory }

func (b *Memory) Update(fn func(Tx) error) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	// Отката нет: Service меняет данные только после всех проверок, ошибка до Put ничего не оставляет.
	return fn(memTx{b})
}
func id(p, k string) string { return p + "\x00" + k }
func (t memTx) Get(p, k string) (*Row, error) {
	if r, ok := t.m.rows[id(p, k)]; ok {
		return &r, nil
	}
	return nil, nil
}
func (t memTx) Put(r Row) error { t.m.rows[id(r.Plugin, r.Key)] = r; return nil }
func (t memTx) Delete(p, k string) (bool, error) {
	_, ok := t.m.rows[id(p, k)]
	delete(t.m.rows, id(p, k))
	return ok, nil
}
func (t memTx) List(p, prefix string, limit int) ([]Row, error) {
	var o []Row
	for _, r := range t.m.rows {
		if r.Plugin == p && strings.HasPrefix(r.Key, prefix) {
			o = append(o, r)
		}
	}
	sort.Slice(o, func(i, j int) bool { return o[i].Key < o[j].Key })
	if len(o) > limit {
		o = o[:limit]
	}
	return o, nil
}
func (t memTx) Stats(p string) (int64, int64, error) {
	var n, b int64
	for _, r := range t.m.rows {
		if r.Plugin == p {
			n++
			b += int64(len(r.Value))
		}
	}
	return n, b, nil
}
func (t memTx) Clear(p string) error {
	for k, r := range t.m.rows {
		if r.Plugin == p {
			delete(t.m.rows, k)
		}
	}
	return nil
}
