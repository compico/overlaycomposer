// PoC хоста серверных wasm-плагинов: wazero + тот же ABI, что проверен в ../node/harness.mjs.
//
// Проверен вручную (Windows, wazero v1.12.0): маршруты, лог плагина, storage, паника → 500, перехват stderr с трассой,
// перезапуск экземпляра с сохранением состояния. go.mod/go.sum зафиксируйте при первом `go mod tidy`. Запуск:
//
//	cd ../guest && GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o hello.wasm .
//	cd ../host && go mod tidy && go run . ../guest/hello.wasm
//	curl 'http://127.0.0.1:8099/plugins/hello/greet?name=Ann'
//	curl 'http://127.0.0.1:8099/plugins/hello/boom'   # паника плагина: хост жив, плагин перезапущен
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

type route struct{ Method, Path, Handler string }

type meta struct {
	ID         string  `json:"id"`
	Version    string  `json:"version"`
	APIVersion int     `json:"apiVersion"`
	Routes     []route `json:"routes"`
}

type request struct {
	Method string            `json:"method"`
	Path   string            `json:"path"`
	Query  map[string]string `json:"query"`
}

type response struct {
	Status int            `json:"status"`
	Body   map[string]any `json:"body"`
}

// plugin — один плагин: скомпилированный модуль и текущий экземпляр. Вызовы сериализуются мьютексом
// (экземпляр однопоточный); состояние плагина лежит в storage хоста, поэтому перезапуск экземпляра его не теряет.
type plugin struct {
	rt       wazero.Runtime
	compiled wazero.CompiledModule
	log      *slog.Logger

	mu   sync.Mutex
	mod  api.Module
	meta meta

	kvMu sync.Mutex
	kv   map[string]string
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: host <plugin.wasm>")
		os.Exit(2)
	}
	ctx := context.Background()
	wasm, err := os.ReadFile(os.Args[1])
	if err != nil {
		panic(err)
	}
	p, err := load(ctx, wasm)
	if err != nil {
		panic(err)
	}
	mux := http.NewServeMux()
	for _, r := range p.meta.Routes {
		r := r
		mux.HandleFunc("/plugins/"+p.meta.ID+r.Path, func(w http.ResponseWriter, req *http.Request) {
			if req.Method != r.Method {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			q := map[string]string{}
			for k, v := range req.URL.Query() {
				q[k] = v[0]
			}
			resp, err := p.call(req.Context(), r.Handler, request{Method: req.Method, Path: req.URL.Path, Query: q})
			if err != nil {
				http.Error(w, "plugin error", http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(resp.Status)
			_ = json.NewEncoder(w).Encode(resp.Body)
		})
	}
	slog.Info("listening", "addr", "127.0.0.1:8099", "plugin", p.meta.ID)
	_ = http.ListenAndServe("127.0.0.1:8099", mux)
}

func load(ctx context.Context, wasm []byte) (*plugin, error) {
	rt := wazero.NewRuntime(ctx)
	wasi_snapshot_preview1.MustInstantiate(ctx, rt)
	p := &plugin{rt: rt, kv: map[string]string{}, log: slog.Default().With("plugin", "?")}
	if _, err := rt.NewHostModuleBuilder("oc").NewFunctionBuilder().WithFunc(p.hostCall).Export("host_call").Instantiate(ctx); err != nil {
		return nil, err
	}
	c, err := rt.CompileModule(ctx, wasm)
	if err != nil {
		return nil, err
	}
	p.compiled = c
	return p, p.start(ctx)
}

// start создаёт экземпляр и вызывает oc_init. Вывод плагина в stderr (паники, fmt.Fprintln) идёт в лог с id плагина.
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
	cfg := wazero.NewModuleConfig().WithName("").WithStderr(pw).WithStdout(pw).WithStartFunctions("_initialize")
	mod, err := p.rt.InstantiateModule(ctx, p.compiled, cfg)
	if err != nil {
		return err
	}
	p.mod = mod
	out, err := p.invoke(ctx, "oc_init", []byte(`{"apiVersion":1,"config":{}}`), nil)
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

// put кладёт байты в память плагина (через его alloc) и возвращает указатель.
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

// take читает буфер, который вернул плагин (i64 = ptr<<32|len), и освобождает его.
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

// invoke вызывает oc_init (name == "oc_init", arg) либо oc_call (handler в name, arg).
func (p *plugin) invoke(ctx context.Context, fn string, arg []byte, handler []byte) ([]byte, error) {
	m := p.mod
	var res []uint64
	var err error
	if fn == "oc_init" {
		ap, e := p.put(ctx, m, arg)
		if e != nil {
			return nil, e
		}
		res, err = m.ExportedFunction("oc_init").Call(ctx, uint64(ap), uint64(len(arg)))
		_, _ = m.ExportedFunction("dealloc").Call(ctx, uint64(ap))
	} else {
		np, e := p.put(ctx, m, handler)
		if e != nil {
			return nil, e
		}
		ap, e := p.put(ctx, m, arg)
		if e != nil {
			return nil, e
		}
		res, err = m.ExportedFunction("oc_call").Call(ctx, uint64(np), uint64(len(handler)), uint64(ap), uint64(len(arg)))
		_, _ = m.ExportedFunction("dealloc").Call(ctx, uint64(np))
		_, _ = m.ExportedFunction("dealloc").Call(ctx, uint64(ap))
	}
	if err != nil {
		return nil, err
	}
	return p.take(ctx, m, res[0])
}

// call вызывает обработчик плагина. Любая ошибка выполнения (trap, паника) — экземпляр считается испорченным
// и пересоздаётся, остальной хост продолжает работать.
func (p *plugin) call(ctx context.Context, handler string, req request) (response, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	arg, _ := json.Marshal(req)
	out, err := p.invoke(ctx, "oc_call", arg, []byte(handler))
	if err != nil {
		p.log.Error("plugin trapped, restarting", "handler", handler, "err", err)
		_ = p.mod.Close(ctx)
		if serr := p.start(ctx); serr != nil {
			p.log.Error("plugin restart failed", "err", serr)
		}
		return response{}, err
	}
	var resp response
	if err := json.Unmarshal(out, &resp); err != nil {
		return response{}, err
	}
	return resp, nil
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
	var out any
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
		p.kvMu.Lock()
		out = map[string]string{"value": p.kv[a.Key]}
		p.kvMu.Unlock()
	case "storage.set":
		var a struct{ Key, Value string }
		_ = json.Unmarshal(arg, &a)
		p.kvMu.Lock()
		p.kv[a.Key] = a.Value
		p.kvMu.Unlock()
		out = map[string]string{}
	default:
		out = map[string]string{"error": "unknown op " + op}
	}
	data, _ := json.Marshal(out)
	ptr, err := p.put(ctx, m, data) // вложенный вызов alloc плагина из host_call (в Node-проверке это работает)
	if err != nil {
		return 0
	}
	return uint64(ptr)<<32 | uint64(len(data))
}
