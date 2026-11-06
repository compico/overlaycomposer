package wasmplugins

import (
	"context"
	"io"
)

// HostCall — единственный вход плагина в хост: операция и JSON-аргумент → JSON-ответ (nil — пустой ответ).
type HostCall func(ctx context.Context, op string, arg []byte) []byte

// Engine/Module/Instance — граница с wasm-рантаймом. Всё остальное в пакете от рантайма не зависит,
// поэтому логика (права, события, таймеры, перезапуск) проверяется на фейковом движке без wazero.
type Engine interface {
	// Compile компилирует wasm; ошибка — файл не wasm или нет обязательных экспортов.
	Compile(ctx context.Context, wasm []byte) (Module, error)
	Close(ctx context.Context) error
}

// Module — скомпилированный плагин, из которого создаются экземпляры.
type Module interface {
	// New создаёт экземпляр. Всё, что плагин пишет в stdout/stderr, идёт в out.
	New(ctx context.Context, host HostCall, out io.Writer) (Instance, error)
	Close(ctx context.Context) error
}

// Instance не потокобезопасен: вызывающий сериализует обращения.
type Instance interface {
	// Meta вызывает чистую функцию oc_meta и возвращает JSON метаданных.
	Meta(ctx context.Context) ([]byte, error)
	// Init вызывает oc_init({apiVersion, config}).
	Init(ctx context.Context, arg []byte) ([]byte, error)
	// Call вызывает oc_call(name, arg): обработчик маршрута или on_event.
	Call(ctx context.Context, name string, arg []byte) ([]byte, error)
	Close(ctx context.Context) error
}
