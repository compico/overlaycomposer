package wasmplugins

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

// fakeEngine — движок без wasm: «файл» — это ключ в таблице модулей, поведение задаёт тест.
type fakeEngine struct {
	mu       sync.Mutex
	mods     map[string]*fakeModule
	compiles int
}

func (e *fakeEngine) Compile(_ context.Context, wasm []byte) (Module, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.compiles++
	m := e.mods[string(wasm)]
	if m == nil {
		return nil, errors.New("not a valid wasm module")
	}
	return m, nil
}
func (e *fakeEngine) Close(context.Context) error { return nil }
func (e *fakeEngine) compileCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.compiles
}

type fakeModule struct {
	meta    Meta
	rawMeta []byte // если задан — отдаётся вместо meta
	handler func(in *fakeInst, name string, arg []byte) ([]byte, error)

	mu        sync.Mutex
	instances []*fakeInst
	closed    bool
}

func (m *fakeModule) New(_ context.Context, host HostCall, out io.Writer) (Instance, error) {
	in := &fakeInst{m: m, host: host, out: out}
	m.mu.Lock()
	m.instances = append(m.instances, in)
	m.mu.Unlock()
	return in, nil
}
func (m *fakeModule) Close(context.Context) error {
	m.mu.Lock()
	m.closed = true
	m.mu.Unlock()
	return nil
}
func (m *fakeModule) count() int { m.mu.Lock(); defer m.mu.Unlock(); return len(m.instances) }

type fakeInst struct {
	m      *fakeModule
	host   HostCall
	out    io.Writer
	config json.RawMessage
	mu     sync.Mutex
	closed bool
}

func (i *fakeInst) Meta(context.Context) ([]byte, error) {
	if i.m.rawMeta != nil {
		return i.m.rawMeta, nil
	}
	return json.Marshal(i.m.meta)
}
func (i *fakeInst) Init(_ context.Context, arg []byte) ([]byte, error) {
	var a struct {
		Config json.RawMessage `json:"config"`
	}
	_ = json.Unmarshal(arg, &a)
	i.config = a.Config
	return nil, nil
}
func (i *fakeInst) Call(ctx context.Context, name string, arg []byte) ([]byte, error) {
	return i.m.handler(i, name, arg)
}
func (i *fakeInst) Close(context.Context) error {
	i.mu.Lock()
	i.closed = true
	i.mu.Unlock()
	return nil
}

// call — хелпер для обработчика: вызвать host_call.
func (i *fakeInst) hc(op string, arg any) map[string]any {
	b, _ := json.Marshal(arg)
	out := i.host(context.Background(), op, b)
	var m map[string]any
	_ = json.Unmarshal(out, &m)
	return m
}

type memStorage struct {
	mu sync.Mutex
	m  map[string]json.RawMessage
}

func (s *memStorage) Get(_ context.Context, p, k string) (json.RawMessage, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[p+"/"+k]
	return v, ok, nil
}
func (s *memStorage) Set(_ context.Context, p, k string, v json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = map[string]json.RawMessage{}
	}
	s.m[p+"/"+k] = v
	return nil
}
func (s *memStorage) Delete(_ context.Context, p, k string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, p+"/"+k)
	return nil
}

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func baseMeta(id string) Meta {
	return Meta{ID: id, Name: id, Version: "1.0.0", APIVersion: APIVersion}
}

// newTestPlugin собирает запущенный плагин без менеджера.
func newTestPlugin(t *testing.T, mod *fakeModule, svc Services, grants ...string) *Plugin {
	t.Helper()
	if svc.Log == nil {
		svc.Log = quietLog()
	}
	p := newPlugin(context.Background(), svc, mod, mod.meta, "x.wasm", "sha", nil, grants)
	if err := p.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Stop)
	return p
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timeout: %s", what)
}
