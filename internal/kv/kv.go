// Package kv — хранилище данных плагинов: JSON-значения по ключам, отдельное пространство имён на плагин,
// ревизии для безопасного «прочитал-изменил-записал» и квоты. Логика не зависит от базы (интерфейс Backend).
package kv

import (
	"bytes"
	"encoding/json"
	"errors"
	"regexp"
	"time"
)

const (
	MaxKeyLen   = 128
	MaxValueLen = 256 << 10 // байт JSON на одно значение
	MaxKeys     = 5000      // ключей на плагин
	MaxBytes    = 8 << 20   // всего байт значений на плагин
	DefaultList = 100
	MaxList     = 1000
)

var (
	keyRe = regexp.MustCompile(`^[A-Za-z0-9_.:/-]{1,128}$`)

	ErrBadKey   = errors.New("invalid key (1–128 chars: letters, digits, _ . : / -)")
	ErrBadValue = errors.New("value must be valid JSON up to 256 KB")
	ErrConflict = errors.New("conflict: the value was changed by someone else")
	ErrQuota    = errors.New("storage quota exceeded")
	ErrBadPlug  = errors.New("invalid plugin id")
)

var pluginRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// Row — запись в хранилище.
type Row struct {
	Plugin    string
	Key       string
	Value     string // JSON
	Rev       int64  // растёт на 1 при каждой записи; у несуществующего ключа 0
	UpdatedAt time.Time
}

// Tx — операции внутри одной (сериализованной) транзакции.
type Tx interface {
	Get(plugin, key string) (*Row, error)
	Put(Row) error
	Delete(plugin, key string) (bool, error)
	List(plugin, prefix string, limit int) ([]Row, error) // по возрастанию ключа
	Stats(plugin string) (keys, bytes int64, err error)
	Clear(plugin string) error
}

type Backend interface {
	Update(fn func(Tx) error) error
}

// Change — изменение, о котором узнают остальные копии плагина.
type Change struct {
	Plugin  string
	Key     string
	Deleted bool
	Value   json.RawMessage
	Rev     int64
}

type Service struct {
	b        Backend
	OnChange func(Change) // вызывается после успешной записи/удаления (вне транзакции)
	now      func() time.Time
}

func New(b Backend) *Service { return &Service{b: b, now: time.Now} }

func checkPlugin(p string) error {
	if !pluginRe.MatchString(p) {
		return ErrBadPlug
	}
	return nil
}

// Entry — значение с ревизией.
type Entry struct {
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value"`
	Rev   int64           `json:"rev"`
}

// Get возвращает запись; found=false — ключа нет (rev 0).
func (s *Service) Get(plugin, key string) (e Entry, found bool, err error) {
	if err = checkPlugin(plugin); err != nil {
		return
	}
	if !keyRe.MatchString(key) {
		return e, false, ErrBadKey
	}
	err = s.b.Update(func(tx Tx) error {
		r, err := tx.Get(plugin, key)
		if err != nil || r == nil {
			return err
		}
		e, found = Entry{Key: key, Value: json.RawMessage(r.Value), Rev: r.Rev}, true
		return nil
	})
	return
}

// Set записывает значение. ifRev != nil — записать, только если текущая ревизия равна *ifRev (0 — «ключа ещё нет»).
// Возвращает новую ревизию.
func (s *Service) Set(plugin, key string, value json.RawMessage, ifRev *int64) (int64, error) {
	if err := checkPlugin(plugin); err != nil {
		return 0, err
	}
	if !keyRe.MatchString(key) {
		return 0, ErrBadKey
	}
	if len(value) == 0 || len(value) > MaxValueLen || !json.Valid(value) {
		return 0, ErrBadValue
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, value); err != nil { // без лишних пробелов: квота считается по реальному размеру
		return 0, ErrBadValue
	}
	compact := buf.String()
	var rev int64
	var change Change
	err := s.b.Update(func(tx Tx) error {
		cur, err := tx.Get(plugin, key)
		if err != nil {
			return err
		}
		var curRev int64
		var curLen int64
		if cur != nil {
			curRev, curLen = cur.Rev, int64(len(cur.Value))
		}
		if ifRev != nil && *ifRev != curRev {
			return ErrConflict
		}
		keys, bytes, err := tx.Stats(plugin)
		if err != nil {
			return err
		}
		if cur == nil {
			keys++
		}
		bytes += int64(len(compact)) - curLen
		if keys > MaxKeys || bytes > MaxBytes {
			return ErrQuota
		}
		rev = curRev + 1
		if err := tx.Put(Row{Plugin: plugin, Key: key, Value: compact, Rev: rev, UpdatedAt: s.now()}); err != nil {
			return err
		}
		change = Change{Plugin: plugin, Key: key, Value: json.RawMessage(compact), Rev: rev}
		return nil
	})
	if err != nil {
		return 0, err
	}
	s.notify(change)
	return rev, nil
}

// Delete удаляет ключ; false — его не было.
func (s *Service) Delete(plugin, key string) (bool, error) {
	if err := checkPlugin(plugin); err != nil {
		return false, err
	}
	if !keyRe.MatchString(key) {
		return false, ErrBadKey
	}
	var ok bool
	if err := s.b.Update(func(tx Tx) error {
		var err error
		ok, err = tx.Delete(plugin, key)
		return err
	}); err != nil {
		return false, err
	}
	if ok {
		s.notify(Change{Plugin: plugin, Key: key, Deleted: true})
	}
	return ok, nil
}

// List возвращает записи с ключами, начинающимися с prefix (по возрастанию ключа).
func (s *Service) List(plugin, prefix string, limit int) ([]Entry, error) {
	if err := checkPlugin(plugin); err != nil {
		return nil, err
	}
	if prefix != "" && !keyRe.MatchString(prefix) {
		return nil, ErrBadKey
	}
	if limit <= 0 {
		limit = DefaultList
	}
	limit = min(limit, MaxList)
	out := []Entry{}
	err := s.b.Update(func(tx Tx) error {
		rows, err := tx.List(plugin, prefix, limit)
		if err != nil {
			return err
		}
		for _, r := range rows {
			out = append(out, Entry{Key: r.Key, Value: json.RawMessage(r.Value), Rev: r.Rev})
		}
		return nil
	})
	return out, err
}

type Stats struct {
	Keys  int64 `json:"keys"`
	Bytes int64 `json:"bytes"`
}

func (s *Service) Stats(plugin string) (st Stats, err error) {
	if err = checkPlugin(plugin); err != nil {
		return
	}
	err = s.b.Update(func(tx Tx) error {
		st.Keys, st.Bytes, err = tx.Stats(plugin)
		return err
	})
	return
}

// Clear стирает все данные плагина (кнопка в редакторе).
func (s *Service) Clear(plugin string) error {
	if err := checkPlugin(plugin); err != nil {
		return err
	}
	var keys []string
	err := s.b.Update(func(tx Tx) error {
		rows, err := tx.List(plugin, "", MaxKeys+1)
		if err != nil {
			return err
		}
		for _, r := range rows {
			keys = append(keys, r.Key)
		}
		return tx.Clear(plugin)
	})
	if err != nil {
		return err
	}
	for _, k := range keys {
		s.notify(Change{Plugin: plugin, Key: k, Deleted: true})
	}
	return nil
}

func (s *Service) notify(c Change) {
	if s.OnChange != nil && c.Plugin != "" {
		s.OnChange(c)
	}
}
