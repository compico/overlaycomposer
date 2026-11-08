package wasmplugins

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/compico/overlaycomposer/internal/wasmassets"
)

const (
	maxWasmBytes   = wasmassets.MaxWasmBytes
	maxConfigBytes = 64 << 10
)

// ErrNoPlugin — плагина с таким id нет.
var ErrNoPlugin = errors.New("no such plugin")

// PluginState — то, что пользователь настроил для плагина и что переживает перезапуск приложения.
type PluginState struct {
	Granted []string        `json:"granted"`
	Config  json.RawMessage `json:"config,omitempty"`
}

// StateStore хранит PluginState (в OC — в настройках приложения).
type StateStore interface {
	Load(id string) (PluginState, error)
	Save(id string, st PluginState) error
}

// Problem — файл, который не удалось загрузить как плагин, и причина (показывается пользователю).
type Problem struct {
	File  string `json:"file"`
	Error string `json:"error"`
}

// Options — настройки менеджера.
type Options struct {
	Engine   Engine
	Dirs     []string // папки с .wasm: ocplugs рядом с exe и т.п.; несуществующие пропускаются
	Services Services
	State    StateStore
	// Reserved — id, которые заняты чем-то другим (HTML-плагины): wasm-плагин с таким id не загружается.
	Reserved func(id string) bool
}

// Manager находит плагины в папках, загружает их и раздаёт им маршруты и события.
type Manager struct {
	opts Options
	log  *slog.Logger
	ctx  context.Context

	scanMu   sync.Mutex // сериализует Scan
	mu       sync.RWMutex
	plugins  map[string]*Plugin
	problems []Problem
}

func NewManager(ctx context.Context, opts Options) *Manager {
	log := opts.Services.Log
	if log == nil {
		log = slog.Default()
		opts.Services.Log = log
	}
	return &Manager{opts: opts, log: log, ctx: ctx, plugins: map[string]*Plugin{}}
}

type candidate struct {
	path string
	data []byte
	sha  string
}

// Scan читает папки и приводит набор запущенных плагинов в соответствие с файлами: новые запускаются,
// изменённые перезапускаются, исчезнувшие останавливаются; неизменённые не трогаются.
func (m *Manager) Scan() {
	m.scanMu.Lock()
	defer m.scanMu.Unlock()

	var problems []Problem
	var cands []candidate
	for _, dir := range m.opts.Dirs {
		ents, err := os.ReadDir(dir)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				problems = append(problems, Problem{File: dir, Error: err.Error()})
			}
			continue
		}
		for _, e := range ents {
			if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".wasm") || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			path := filepath.Join(dir, e.Name())
			data, err := readLimited(path)
			if err != nil {
				problems = append(problems, Problem{File: path, Error: err.Error()})
				continue
			}
			sum := sha256.Sum256(data)
			cands = append(cands, candidate{path: path, data: data, sha: hex.EncodeToString(sum[:])})
		}
	}

	keep := map[string]bool{}
	for _, c := range cands {
		if id, ok := m.unchanged(c); ok {
			keep[id] = true
			continue
		}
		p, err := m.load(c)
		if err != nil {
			m.log.Warn("plugin skipped", "file", c.path, "err", err)
			problems = append(problems, Problem{File: c.path, Error: err.Error()})
			continue
		}
		id := p.meta.ID
		if keep[id] {
			p.cancel() // не запускался: останавливать нечего, только освободить модуль
			_ = p.mod.Close(m.ctx)
			problems = append(problems, Problem{File: c.path, Error: fmt.Sprintf("duplicate plugin id %q: the first file is used", id)})
			continue
		}
		keep[id] = true
		m.mu.Lock()
		old := m.plugins[id]
		m.plugins[id] = p
		m.mu.Unlock()
		if old != nil {
			old.Stop()
		}
		_ = p.Start() // сбой запуска виден в состоянии failed
	}

	var gone []*Plugin
	m.mu.Lock()
	for id, p := range m.plugins {
		if !keep[id] {
			gone = append(gone, p)
			delete(m.plugins, id)
		}
	}
	m.problems = problems
	m.mu.Unlock()
	for _, p := range gone {
		p.Stop()
	}
}

func readLimited(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxWasmBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxWasmBytes {
		return nil, fmt.Errorf("file is larger than %d MB", maxWasmBytes>>20)
	}
	return b, nil
}

func (m *Manager) unchanged(c candidate) (string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for id, p := range m.plugins {
		if p.file == c.path && p.sha == c.sha {
			return id, true
		}
	}
	return "", false
}

// load компилирует модуль, читает и проверяет oc_meta и собирает Plugin (ещё не запущенный).
func (m *Manager) load(c candidate) (*Plugin, error) {
	mod, err := m.opts.Engine.Compile(m.ctx, c.data)
	if err != nil {
		return nil, err
	}
	meta, err := readMeta(m.ctx, mod)
	if err != nil {
		_ = mod.Close(m.ctx)
		return nil, err
	}
	if m.opts.Reserved != nil && m.opts.Reserved(meta.ID) {
		_ = mod.Close(m.ctx)
		return nil, fmt.Errorf("id %q is already used by another plugin", meta.ID)
	}
	assets, err := wasmassets.Of(c.data)
	if err != nil {
		_ = mod.Close(m.ctx)
		return nil, err
	}
	for _, w := range meta.Widgets {
		if !assets.Has(w.Entry) {
			_ = mod.Close(m.ctx)
			return nil, fmt.Errorf("widget %q: entry %q is not in the plugin's %s section", w.ID, w.Entry, wasmassets.Section)
		}
	}
	var st PluginState
	if m.opts.State != nil {
		if st, err = m.opts.State.Load(meta.ID); err != nil {
			m.log.Warn("plugin state is unreadable, starting without grants", "plugin", meta.ID, "err", err)
			st = PluginState{}
		}
	}
	p := newPlugin(m.ctx, m.opts.Services, mod, *meta, c.path, c.sha, st.Config, st.Granted)
	p.assets = assets
	return p, nil
}

// Asset отдаёт файл статики плагина (из секции oc.assets). ok=false — нет плагина или файла.
func (m *Manager) Asset(id, p string) (data []byte, mime string, sha string, ok bool) {
	pl, err := m.get(id)
	if err != nil {
		return nil, "", "", false
	}
	data, mime, ok = pl.assets.Get(p)
	return data, mime, pl.sha, ok
}

func stubHost(context.Context, string, []byte) []byte {
	return errOut("host is not available while reading oc_meta")
}

// readMeta вызывает oc_meta на одноразовом экземпляре: он ничего не может, кроме как вернуть метаданные.
func readMeta(ctx context.Context, mod Module) (*Meta, error) {
	inst, err := mod.New(ctx, stubHost, io.Discard)
	if err != nil {
		return nil, fmt.Errorf("instantiate: %w", err)
	}
	defer inst.Close(ctx)
	cctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	raw, err := inst.Meta(cctx)
	if err != nil {
		return nil, fmt.Errorf("oc_meta: %w", err)
	}
	if len(raw) == 0 || len(raw) > maxMetaBytes {
		return nil, errors.New("oc_meta: empty or too large response")
	}
	var meta Meta
	if err := json.Unmarshal(raw, &meta); err != nil {
		return nil, fmt.Errorf("oc_meta: %w", err)
	}
	if err := meta.Validate(); err != nil {
		return nil, fmt.Errorf("oc_meta: %w", err)
	}
	return &meta, nil
}

func (m *Manager) get(id string) (*Plugin, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	p := m.plugins[id]
	if p == nil {
		return nil, ErrNoPlugin
	}
	return p, nil
}

// Dirs — папки, в которых менеджер ищет плагины (первая — основная: её открывает интерфейс).
func (m *Manager) Dirs() []string { return append([]string(nil), m.opts.Dirs...) }

// Has — есть ли загруженный wasm-плагин с таким id.
func (m *Manager) Has(id string) bool {
	_, err := m.get(id)
	return err == nil
}

// List — плагины и файлы, которые не удалось загрузить.
func (m *Manager) List() ([]Info, []Problem) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Info, 0, len(m.plugins))
	for _, p := range m.plugins {
		out = append(out, p.Info())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Meta.ID < out[j].Meta.ID })
	return out, append([]Problem(nil), m.problems...)
}

// Handle направляет HTTP-запрос в маршрут плагина.
func (m *Manager) Handle(ctx context.Context, id string, req Request) (Response, error) {
	p, err := m.get(id)
	if err != nil {
		return Response{}, err
	}
	return p.Handle(ctx, req)
}

// Publish рассылает событие подписанным плагинам, у которых есть нужное разрешение. Возвращает, скольким доставлено.
func (m *Manager) Publish(typ string, data any) int {
	m.mu.RLock()
	ps := make([]*Plugin, 0, len(m.plugins))
	for _, p := range m.plugins {
		ps = append(ps, p)
	}
	m.mu.RUnlock()
	n := 0
	for _, p := range ps {
		if p.Deliver(typ, data) {
			n++
		}
	}
	return n
}

// Wants — есть ли хоть один плагин, готовый принять событие: по нему можно решить, нужен ли хосту EventSub.
func (m *Manager) Wants(typ string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, p := range m.plugins {
		if p.meta.Subscribes(typ) && (typ != EventRedemption || p.granted(PermRedeem)) {
			return true
		}
	}
	return false
}

// SetGrants заменяет набор выданных разрешений (можно выдать только то, что плагин запросил). Если набор изменился,
// плагин перезапускается: OnInit выполняется заново уже с новыми правами.
func (m *Manager) SetGrants(id string, perms []string) error {
	p, err := m.get(id)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	var clean []string
	for _, perm := range perms {
		if !KnownPermission(perm) {
			return fmt.Errorf("unknown permission %q", perm)
		}
		if !p.meta.Requests(perm) {
			return fmt.Errorf("plugin did not request permission %q", perm)
		}
		if !seen[perm] {
			seen[perm] = true
			clean = append(clean, perm)
		}
	}
	if err := m.save(id, PluginState{Granted: clean, Config: p.config()}); err != nil {
		return err
	}
	before := p.Info().Granted
	p.setGrants(clean)
	if !sameSet(before, p.Info().Granted) {
		// oc_init уже отработал с прежними правами (например, не смог попросить таймер): запускаем заново.
		// Сбой запуска виден в состоянии плагина (failed + причина), поэтому не ошибка выдачи прав.
		if err := p.ResetFailure(); err != nil {
			m.log.Warn("plugin did not restart after a permission change", "plugin", id, "err", err)
		}
	}
	return nil
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]bool{}
	for _, x := range a {
		seen[x] = true
	}
	for _, x := range b {
		if !seen[x] {
			return false
		}
	}
	return true
}

// SetConfig сохраняет настройки (JSON-объект) и перезапускает экземпляр, чтобы плагин получил их в oc_init.
func (m *Manager) SetConfig(id string, cfg json.RawMessage) error {
	p, err := m.get(id)
	if err != nil {
		return err
	}
	cfg = bytes.TrimSpace(cfg)
	if len(cfg) > maxConfigBytes {
		return fmt.Errorf("config is too large (max %d KB)", maxConfigBytes>>10)
	}
	var obj map[string]any
	if err := json.Unmarshal(cfg, &obj); err != nil || obj == nil {
		return errors.New("config must be a JSON object")
	}
	if err := m.save(id, PluginState{Granted: p.Info().Granted, Config: cfg}); err != nil {
		return err
	}
	p.setConfig(cfg)
	return p.ResetFailure()
}

// Restart перезапускает плагин (в том числе вышедший из строя).
func (m *Manager) Restart(id string) error {
	p, err := m.get(id)
	if err != nil {
		return err
	}
	return p.ResetFailure()
}

func (m *Manager) save(id string, st PluginState) error {
	if m.opts.State == nil {
		return nil
	}
	return m.opts.State.Save(id, st)
}

// Close останавливает все плагины и движок.
func (m *Manager) Close() {
	m.scanMu.Lock()
	defer m.scanMu.Unlock()
	m.mu.Lock()
	ps := m.plugins
	m.plugins = map[string]*Plugin{}
	m.mu.Unlock()
	for _, p := range ps {
		p.Stop()
	}
	_ = m.opts.Engine.Close(context.Background())
}

// MemState — StateStore в памяти (тесты и запуск без настроек).
type MemState struct {
	mu sync.Mutex
	m  map[string]PluginState
}

func (s *MemState) Load(id string) (PluginState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m[id], nil
}

func (s *MemState) Save(id string, st PluginState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = map[string]PluginState{}
	}
	s.m[id] = st
	return nil
}
