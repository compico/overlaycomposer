// Package logx — лог в файл (у Windows-сборки без консоли это единственное место, где видны сообщения).
// Ротация: при превышении лимита файл переименовывается в .1 (хранится одно старое).
package logx

import (
	"io"
	"log"
	"log/slog"
	"os"
	"sync"
)

const maxBytes = 5 * 1024 * 1024

type rotatingFile struct {
	mu      sync.Mutex
	path    string
	f       *os.File
	written int64
}

func openRotating(path string) (*rotatingFile, error) {
	r := &rotatingFile{path: path}
	if st, err := os.Stat(path); err == nil {
		r.written = st.Size()
	}
	if err := r.open(); err != nil {
		return nil, err
	}
	if r.written > maxBytes {
		r.rotate()
	}
	return r, nil
}

func (r *rotatingFile) open() error {
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	r.f = f
	return nil
}

func (r *rotatingFile) rotate() {
	if r.f != nil {
		_ = r.f.Close()
	}
	_ = os.Rename(r.path, r.path+".1")
	r.written = 0
	_ = r.open()
}

func (r *rotatingFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return len(p), nil
	}
	n, err := r.f.Write(p)
	r.written += int64(n)
	if r.written > maxBytes {
		r.rotate()
	}
	return n, err
}

func (r *rotatingFile) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return nil
	}
	err := r.f.Close()
	r.f = nil
	return err
}

var current *rotatingFile

// Init подключает файл лога к slog и стандартному log. echo — дублировать в stderr (если консоль есть).
func Init(path string, echo bool) error {
	f, err := openRotating(path)
	if err != nil {
		return err
	}
	current = f
	var w io.Writer = f
	if echo {
		w = io.MultiWriter(f, os.Stderr)
	}
	h := slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelDebug})
	slog.SetDefault(slog.New(h))
	log.SetOutput(w) // сторонние пакеты, пишущие через стандартный log
	log.SetFlags(log.LstdFlags)
	return nil
}

func Close() {
	if current != nil {
		_ = current.Close()
	}
}
