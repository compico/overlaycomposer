package wasmplugins

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"github.com/compico/overlaycomposer/internal/kv"
)

// KVStorage — Storage поверх kv.Service: у каждого плагина своё пространство имён (его id), квоты и ревизии общие с остальным kv.
type KVStorage struct{ S *kv.Service }

func (k KVStorage) Get(_ context.Context, plugin, key string) (json.RawMessage, bool, error) {
	e, found, err := k.S.Get(plugin, key)
	if err != nil || !found {
		return nil, false, err
	}
	return e.Value, true, nil
}

func (k KVStorage) Set(_ context.Context, plugin, key string, value json.RawMessage) error {
	_, err := k.S.Set(plugin, key, value, nil)
	return err
}

func (k KVStorage) Delete(_ context.Context, plugin, key string) error {
	_, err := k.S.Delete(plugin, key)
	return err
}

// FileState — StateStore в одном JSON-файле (права и настройки плагинов). Не в kv: настройки могут содержать токены,
// а kv виден виджетам через общий WebSocket. Запись атомарная (временный файл + rename).
type FileState struct {
	path string
	mu   sync.Mutex
	m    map[string]PluginState
}

// OpenFileState читает файл; нет файла — пустое состояние, битый файл — ошибка (чтобы не затереть его молча).
func OpenFileState(path string) (*FileState, error) {
	s := &FileState{path: path, m: map[string]PluginState{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &s.m); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *FileState) Load(id string) (PluginState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m[id], nil
}

func (s *FileState) Save(id string, st PluginState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := make(map[string]PluginState, len(s.m)+1)
	for k, v := range s.m {
		next[k] = v
	}
	next[id] = st
	b, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".wasmplugins-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // после успешного Rename файла уже нет
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), s.path); err != nil {
		return err
	}
	s.m = next
	return nil
}
