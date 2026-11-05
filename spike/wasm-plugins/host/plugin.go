package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

type route struct{ Method, Path, Handler string }

type meta struct {
	ID            string   `json:"id"`
	Version       string   `json:"version"`
	APIVersion    int      `json:"apiVersion"`
	Routes        []route  `json:"routes"`
	Subscriptions []string `json:"subscriptions"`
}

type request struct {
	Method string            `json:"method"`
	Path   string            `json:"path"`
	Query  map[string]string `json:"query"`
	Body   json.RawMessage   `json:"body"`
}

type response struct {
	Status int            `json:"status"`
	Body   map[string]any `json:"body"`
}

// plugin — один плагин: скомпилированный модуль и текущий экземпляр. Всё, что исполняет код плагина (маршруты, события,
// таймеры, ответы http.fetch), сериализуется мьютексом: экземпляр однопоточный. Состояние лежит в storage хоста,
// поэтому перезапуск экземпляра его не теряет.
type plugin struct {
	rt       wazero.Runtime
	compiled wazero.CompiledModule
	cfg      json.RawMessage
	log      *slog.Logger
	ctx      context.Context // время жизни плагина: таймеры и запросы останавливаются вместе с ним

	mu   sync.Mutex
	mod  api.Module
	meta meta

	kvMu sync.Mutex
	kv   map[string]string

	tmMu   sync.Mutex
	timers map[string]context.CancelFunc

	updMu   sync.Mutex
	updates []string // заглушка twitch.redemption.update: что плагин просил сделать с погашениями
}

func load(ctx context.Context, wasm []byte, cfg json.RawMessage) (*plugin, error) {
	rt := wazero.NewRuntime(ctx)
	wasi_snapshot_preview1.MustInstantiate(ctx, rt)
	if len(cfg) == 0 {
		cfg = json.RawMessage(`{}`)
	}
	p := &plugin{rt: rt, cfg: cfg, ctx: ctx, kv: map[string]string{}, timers: map[string]context.CancelFunc{}, log: slog.Default().With("plugin", "?")}
	if _, err := rt.NewHostModuleBuilder("oc").NewFunctionBuilder().WithFunc(p.hostCall).Export("host_call").Instantiate(ctx); err != nil {
		return nil, err
	}
	c, err := rt.CompileModule(ctx, wasm)
	if err != nil {
		return nil, err
	}
	p.compiled = c
	p.mu.Lock()
	defer p.mu.Unlock()
	return p, p.start(ctx)
}

// start создаёт экземпляр и вызывает oc_init (под p.mu). Вывод плагина в stderr/stdout идёт в лог с id плагина.
func (p *plugin) start(ctx context.Context) error {
	pr, pw := io.Pipe()
	go func() {
		sc := bufio.NewScanner(pr)
		for sc.Scan() {
			if line := strings.TrimRight(sc.Text(), "\r"); line != "" {
				p.log.Warn("stderr", "line", line)
			}
		}
	}()
	cfg := wazero.NewModuleConfig().WithName("").WithStderr(pw).WithStdout(pw)
	// Reactor-модули (Go c-shared, Rust wasip1 cdylib) имеют _initialize; модули без него (wasm32-unknown-unknown) — нет.
	if _, ok := p.compiled.ExportedFunctions()["_initialize"]; ok {
		cfg = cfg.WithStartFunctions("_initialize")
	} else {
		cfg = cfg.WithStartFunctions()
	}
	mod, err := p.rt.InstantiateModule(ctx, p.compiled, cfg)
	if err != nil {
		return err
	}
	p.mod = mod
	arg, _ := json.Marshal(map[string]any{"apiVersion": 1, "config": p.cfg})
	out, err := p.invokeInit(ctx, arg)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(out, &p.meta); err != nil {
		return fmt.Errorf("oc_init: %w", err)
	}
	if p.meta.APIVersion != 1 {
		return fmt.Errorf("unsupported plugin apiVersion %d", p.meta.APIVersion)
	}
	p.log = slog.Default().With("plugin", p.meta.ID)
	p.log.Info("plugin started", "version", p.meta.Version, "routes", len(p.meta.Routes))
	return nil
}

func (p *plugin) put(ctx context.Context, m api.Module, b []byte) (uint32, error) {
	res, err := m.ExportedFunction("alloc").Call(ctx, uint64(len(b)))
	if err != nil {
		return 0, err
	}
	ptr := uint32(res[0])
	if len(b) > 0 && !m.Memory().Write(ptr, b) {
		return 0, fmt.Errorf("write outside memory")
	}
	return ptr, nil
}

func (p *plugin) take(ctx context.Context, m api.Module, v uint64) ([]byte, error) {
	if v == 0 {
		return nil, nil
	}
	ptr, n := uint32(v>>32), uint32(v)
	b, ok := m.Memory().Read(ptr, n)
	if !ok {
		return nil, fmt.Errorf("read outside memory")
	}
	out := append([]byte(nil), b...)
	_, _ = m.ExportedFunction("dealloc").Call(ctx, uint64(ptr))
	return out, nil
}

func (p *plugin) free(ctx context.Context, ptrs ...uint32) {
	for _, ptr := range ptrs {
		_, _ = p.mod.ExportedFunction("dealloc").Call(ctx, uint64(ptr))
	}
}

func (p *plugin) invokeInit(ctx context.Context, arg []byte) ([]byte, error) {
	ap, err := p.put(ctx, p.mod, arg)
	if err != nil {
		return nil, err
	}
	res, err := p.mod.ExportedFunction("oc_init").Call(ctx, uint64(ap), uint64(len(arg)))
	p.free(ctx, ap)
	if err != nil {
		return nil, err
	}
	return p.take(ctx, p.mod, res[0])
}

func (p *plugin) invokeCall(ctx context.Context, name string, arg []byte) ([]byte, error) {
	np, err := p.put(ctx, p.mod, []byte(name))
	if err != nil {
		return nil, err
	}
	ap, err := p.put(ctx, p.mod, arg)
	if err != nil {
		return nil, err
	}
	res, err := p.mod.ExportedFunction("oc_call").Call(ctx, uint64(np), uint64(len(name)), uint64(ap), uint64(len(arg)))
	p.free(ctx, np, ap)
	if err != nil {
		return nil, err
	}
	return p.take(ctx, p.mod, res[0])
}

// run выполняет вызов плагина под мьютексом. Ошибка выполнения (trap, паника) — экземпляр испорчен: закрываем и создаём заново,
// остальной хост продолжает работать. Остановленный плагин (ctx отменён) не перезапускается.
func (p *plugin) run(ctx context.Context, what string, fn func() ([]byte, error)) ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	out, err := fn()
	if err != nil {
		p.log.Error("plugin trapped, restarting", "what", what, "err", firstLine(err.Error()))
		_ = p.mod.Close(ctx)
		if p.ctx.Err() == nil {
			if serr := p.start(p.ctx); serr != nil {
				p.log.Error("plugin restart failed", "err", serr)
			}
		}
	}
	return out, err
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// call вызывает обработчик маршрута.
func (p *plugin) call(ctx context.Context, handler string, req request) (response, error) {
	arg, _ := json.Marshal(req)
	out, err := p.run(ctx, "handler "+handler, func() ([]byte, error) { return p.invokeCall(ctx, handler, arg) })
	if err != nil {
		return response{}, err
	}
	var resp response
	if err := json.Unmarshal(out, &resp); err != nil {
		return response{}, fmt.Errorf("bad response from plugin: %w", err)
	}
	return resp, nil
}

// deliver доставляет плагину событие (twitch.redemption, timer, http.response, …).
func (p *plugin) deliver(ctx context.Context, typ string, data any) error {
	arg, _ := json.Marshal(map[string]any{"type": typ, "data": data})
	_, err := p.run(ctx, "event "+typ, func() ([]byte, error) { return p.invokeCall(ctx, "on_event", arg) })
	return err
}

func (p *plugin) kvGet(k string) string { p.kvMu.Lock(); defer p.kvMu.Unlock(); return p.kv[k] }
func (p *plugin) kvSet(k, v string)     { p.kvMu.Lock(); p.kv[k] = v; p.kvMu.Unlock() }

// setTimer запускает таймер плагина (повторный вызов с тем же id заменяет прежний — важно при перезапуске экземпляра).
func (p *plugin) setTimer(id string, ms int, repeat bool) {
	if ms < 10 {
		ms = 10
	}
	p.tmMu.Lock()
	defer p.tmMu.Unlock()
	if c := p.timers[id]; c != nil {
		c()
	}
	ctx, cancel := context.WithCancel(p.ctx)
	p.timers[id] = cancel
	go func() {
		t := time.NewTimer(time.Duration(ms) * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				_ = p.deliver(ctx, "timer", map[string]string{"id": id})
				if !repeat {
					return
				}
				t.Reset(time.Duration(ms) * time.Millisecond)
			}
		}
	}()
}

func (p *plugin) clearTimer(id string) {
	p.tmMu.Lock()
	defer p.tmMu.Unlock()
	if c := p.timers[id]; c != nil {
		c()
		delete(p.timers, id)
	}
}

// fetch — асинхронный исходящий HTTP: хост делает запрос в своей горутине и доставляет ответ событием http.response.
// Плагин не ждёт внутри вызова, поэтому долгий запрос не блокирует остальные события и маршруты плагина.
func (p *plugin) fetch(id, method, url string, body []byte) {
	go func() {
		ctx, cancel := context.WithTimeout(p.ctx, 10*time.Second)
		defer cancel()
		res := map[string]any{"id": id}
		req, err := http.NewRequestWithContext(ctx, method, url, strings.NewReader(string(body)))
		if err == nil {
			var resp *http.Response
			if resp, err = http.DefaultClient.Do(req); err == nil {
				b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
				resp.Body.Close()
				res["status"], res["body"] = resp.StatusCode, string(b)
			}
		}
		if err != nil {
			res["error"] = err.Error()
		}
		_ = p.deliver(p.ctx, "http.response", res)
	}()
}

// hostCall — единственная функция, которую плагин импортирует у хоста: операция (строка) и JSON-аргумент → JSON-ответ.
func (p *plugin) hostCall(ctx context.Context, m api.Module, opPtr, opLen, argPtr, argLen uint32) uint64 {
	rd := func(ptr, n uint32) []byte {
		if n == 0 {
			return nil
		}
		b, _ := m.Memory().Read(ptr, n)
		return append([]byte(nil), b...)
	}
	op, arg := string(rd(opPtr, opLen)), rd(argPtr, argLen)
	var out any = map[string]string{}
	switch op {
	case "log":
		var l struct {
			Level  string         `json:"level"`
			Msg    string         `json:"msg"`
			Fields map[string]any `json:"fields"`
		}
		_ = json.Unmarshal(arg, &l)
		attrs := []any{}
		for k, v := range l.Fields {
			attrs = append(attrs, k, v)
		}
		lvl := slog.LevelInfo
		switch strings.ToLower(l.Level) {
		case "warn":
			lvl = slog.LevelWarn
		case "error":
			lvl = slog.LevelError
		}
		p.log.Log(ctx, lvl, l.Msg, attrs...)
		return 0
	case "storage.get":
		var a struct{ Key string }
		_ = json.Unmarshal(arg, &a)
		out = map[string]string{"value": p.kvGet(a.Key)}
	case "storage.set":
		var a struct{ Key, Value string }
		_ = json.Unmarshal(arg, &a)
		p.kvSet(a.Key, a.Value)
	case "timer.set":
		var a struct {
			ID     string `json:"id"`
			MS     int    `json:"ms"`
			Repeat bool   `json:"repeat"`
		}
		_ = json.Unmarshal(arg, &a)
		p.setTimer(a.ID, a.MS, a.Repeat)
	case "timer.clear":
		var a struct{ ID string }
		_ = json.Unmarshal(arg, &a)
		p.clearTimer(a.ID)
	case "http.fetch":
		var a struct {
			ID, Method, URL string
			Body            string
		}
		_ = json.Unmarshal(arg, &a)
		if a.Method == "" {
			a.Method = http.MethodGet
		}
		p.fetch(a.ID, a.Method, a.URL, []byte(a.Body))
	case "twitch.redemption.update": // в PoC — заглушка: в OC это вызов Helix (twitch.Manager.UpdateRedemption)
		var a struct{ RewardID, ID, Status string }
		_ = json.Unmarshal(arg, &a)
		p.updMu.Lock()
		p.updates = append(p.updates, a.ID+":"+a.Status)
		p.updMu.Unlock()
		p.log.Info("twitch.redemption.update (заглушка)", "id", a.ID, "status", a.Status)
	default:
		out = map[string]string{"error": "unknown op " + op}
	}
	data, _ := json.Marshal(out)
	ptr, err := p.put(ctx, m, data) // вложенный вызов alloc плагина из host_call
	if err != nil {
		return 0
	}
	return uint64(ptr)<<32 | uint64(len(data))
}
