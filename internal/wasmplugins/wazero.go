package wasmplugins

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

// maxMemoryPages — потолок памяти экземпляра (64 КБ на страницу): 1024 страницы = 64 МБ.
const maxMemoryPages = 1024

type hostKey struct{}

// WazeroEngine — Engine на wazero (чистый Go, без cgo).
type WazeroEngine struct {
	cache wazero.CompilationCache
}

// NewWazeroEngine создаёт движок. cacheDir — папка для кэша скомпилированного кода (ускоряет старт); пусто — без кэша.
func NewWazeroEngine(cacheDir string) (*WazeroEngine, error) {
	e := &WazeroEngine{}
	if cacheDir != "" {
		c, err := wazero.NewCompilationCacheWithDir(cacheDir)
		if err != nil {
			return nil, err
		}
		e.cache = c
	}
	return e, nil
}

func (e *WazeroEngine) Close(ctx context.Context) error {
	if e.cache != nil {
		return e.cache.Close(ctx)
	}
	return nil
}

func (e *WazeroEngine) Compile(ctx context.Context, wasm []byte) (Module, error) {
	cfg := wazero.NewRuntimeConfig().WithCloseOnContextDone(true).WithMemoryLimitPages(maxMemoryPages)
	if e.cache != nil {
		cfg = cfg.WithCompilationCache(e.cache)
	}
	rt := wazero.NewRuntimeWithConfig(ctx, cfg)
	fail := func(err error) (Module, error) {
		_ = rt.Close(ctx)
		return nil, err
	}
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, rt); err != nil {
		return fail(err)
	}
	// Одна функция host_call на рантайм: нужный HostCall берётся из контекста вызова, поэтому
	// все экземпляры модуля (одноразовый для oc_meta и рабочий) разделяют один host-модуль.
	_, err := rt.NewHostModuleBuilder("oc").NewFunctionBuilder().
		WithFunc(func(ctx context.Context, m api.Module, opPtr, opLen, argPtr, argLen uint32) uint64 {
			host, _ := ctx.Value(hostKey{}).(HostCall)
			if host == nil {
				return 0
			}
			read := func(ptr, n uint32) []byte {
				if n == 0 {
					return nil
				}
				b, _ := m.Memory().Read(ptr, n)
				return append([]byte(nil), b...)
			}
			out := host(ctx, string(read(opPtr, opLen)), read(argPtr, argLen))
			if len(out) == 0 {
				return 0
			}
			ptr, err := alloc(ctx, m, out) // вложенный вызов alloc плагина из host_call
			if err != nil {
				return 0
			}
			return uint64(ptr)<<32 | uint64(len(out))
		}).Export("host_call").Instantiate(ctx)
	if err != nil {
		return fail(err)
	}
	compiled, err := rt.CompileModule(ctx, wasm)
	if err != nil {
		return fail(fmt.Errorf("not a valid wasm module: %w", err))
	}
	exports := compiled.ExportedFunctions()
	for _, name := range []string{"alloc", "dealloc", "oc_meta", "oc_init", "oc_call"} {
		if _, ok := exports[name]; !ok {
			return fail(fmt.Errorf("module does not export %q", name))
		}
	}
	_, reactor := exports["_initialize"]
	return &wazeroModule{rt: rt, compiled: compiled, reactor: reactor}, nil
}

type wazeroModule struct {
	rt       wazero.Runtime
	compiled wazero.CompiledModule
	reactor  bool
}

func (m *wazeroModule) Close(ctx context.Context) error { return m.rt.Close(ctx) }

func (m *wazeroModule) New(ctx context.Context, host HostCall, out io.Writer) (Instance, error) {
	// Без WithSys* wazero отдаёт плагину детерминированные «фальшивые» часы: time.Now() врал бы о дате.
	cfg := wazero.NewModuleConfig().WithName("").WithStdout(out).WithStderr(out)
	// Reactor-модули (Go c-shared, Rust wasip1 cdylib) имеют _initialize; без него (wasm32-unknown-unknown) — нет.
	if m.reactor {
		cfg = cfg.WithStartFunctions("_initialize")
	} else {
		cfg = cfg.WithStartFunctions()
	}
	mod, err := m.rt.InstantiateModule(context.WithValue(ctx, hostKey{}, host), m.compiled, cfg)
	if err != nil {
		return nil, err
	}
	return &wazeroInstance{mod: mod, host: host}, nil
}

type wazeroInstance struct {
	mod  api.Module
	host HostCall
}

func (i *wazeroInstance) Close(ctx context.Context) error { return i.mod.Close(ctx) }

func alloc(ctx context.Context, m api.Module, b []byte) (uint32, error) {
	res, err := m.ExportedFunction("alloc").Call(ctx, uint64(len(b)))
	if err != nil {
		return 0, err
	}
	ptr := uint32(res[0])
	if len(b) > 0 && !m.Memory().Write(ptr, b) {
		return 0, errors.New("write outside memory")
	}
	return ptr, nil
}

func (i *wazeroInstance) free(ctx context.Context, ptrs ...uint32) {
	for _, p := range ptrs {
		_, _ = i.mod.ExportedFunction("dealloc").Call(ctx, uint64(p))
	}
}

// take копирует результат плагина (ptr<<32|len) и освобождает его буфер; 0 — пустой ответ.
func (i *wazeroInstance) take(ctx context.Context, v uint64) ([]byte, error) {
	if v == 0 {
		return nil, nil
	}
	ptr, n := uint32(v>>32), uint32(v)
	b, ok := i.mod.Memory().Read(ptr, n)
	if !ok {
		return nil, errors.New("plugin returned a pointer outside its memory")
	}
	out := append([]byte(nil), b...)
	i.free(ctx, ptr)
	return out, nil
}

func (i *wazeroInstance) with(ctx context.Context) context.Context {
	return context.WithValue(ctx, hostKey{}, i.host)
}

func (i *wazeroInstance) Meta(ctx context.Context) ([]byte, error) {
	ctx = i.with(ctx)
	res, err := i.mod.ExportedFunction("oc_meta").Call(ctx)
	if err != nil {
		return nil, err
	}
	return i.take(ctx, res[0])
}

func (i *wazeroInstance) Init(ctx context.Context, arg []byte) ([]byte, error) {
	ctx = i.with(ctx)
	ap, err := alloc(ctx, i.mod, arg)
	if err != nil {
		return nil, err
	}
	res, err := i.mod.ExportedFunction("oc_init").Call(ctx, uint64(ap), uint64(len(arg)))
	i.free(ctx, ap)
	if err != nil {
		return nil, err
	}
	return i.take(ctx, res[0])
}

func (i *wazeroInstance) Call(ctx context.Context, name string, arg []byte) ([]byte, error) {
	ctx = i.with(ctx)
	np, err := alloc(ctx, i.mod, []byte(name))
	if err != nil {
		return nil, err
	}
	ap, err := alloc(ctx, i.mod, arg)
	if err != nil {
		i.free(ctx, np)
		return nil, err
	}
	res, err := i.mod.ExportedFunction("oc_call").Call(ctx, uint64(np), uint64(len(name)), uint64(ap), uint64(len(arg)))
	i.free(ctx, np, ap)
	if err != nil {
		return nil, err
	}
	return i.take(ctx, res[0])
}
