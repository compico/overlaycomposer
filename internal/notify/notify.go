// Package notify — центр уведомлений: список сообщений для пользователя (новая версия плагина, новая версия приложения),
// который переживает перезапуск (файл data/notifications.json). Пакет не зависит от остального приложения.
package notify

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	KindPluginUpdate = "plugin-update"
	KindAppUpdate    = "app-update"

	maxItems = 100

	// CheckInterval — как часто при запуске проверять обновления плагинов (состояние на диске: перезапуски не считаются новой проверкой).
	CheckInterval = 6 * time.Hour
	// ManualInterval — минимальная пауза между проверками, запущенными кнопкой.
	ManualInterval = time.Minute
	// FailedInterval — после неудачной проверки (нет сети) повторять при запуске не раньше этого срока.
	FailedInterval = time.Hour
)

// Item — одно уведомление. Data — произвольные данные для окна с подробностями (JSON).
type Item struct {
	Key   string          `json:"key"` // стабильный ключ: повторное добавление с тем же ключом не создаёт дубль
	Kind  string          `json:"kind"`
	Title string          `json:"title"`
	Text  string          `json:"text,omitempty"`
	At    int64           `json:"at"` // unix-секунды создания
	Read  bool            `json:"read"`
	Data  json.RawMessage `json:"data,omitempty"`
}

type file struct {
	Items         []Item `json:"items"`
	LastCheck     int64  `json:"lastCheck,omitempty"`     // unix-секунды последней попытки проверки плагинов
	LastCheckFail bool   `json:"lastCheckFail,omitempty"` // попытка не удалась
}

type Center struct {
	path string // "" — только в памяти
	now  func() time.Time

	mu sync.Mutex
	f  file

	checkMu sync.Mutex // одна проверка за раз
}

// Open читает файл (нет файла или он битый — пустой центр).
func Open(path string) *Center {
	c := &Center{path: path, now: time.Now}
	if path != "" {
		if raw, err := os.ReadFile(path); err == nil {
			_ = json.Unmarshal(raw, &c.f)
		}
	}
	return c
}

func (c *Center) saveLocked() {
	if c.path == "" {
		return
	}
	raw, _ := json.Marshal(c.f)
	tmp := c.path + ".tmp"
	if os.WriteFile(tmp, raw, 0o600) == nil {
		_ = os.Rename(tmp, c.path)
	}
}

// Upsert добавляет уведомление; если ключ уже есть — обновляет текст и данные, сохраняя «прочитано» и время.
func (c *Center) Upsert(it Item) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.f.Items {
		if c.f.Items[i].Key == it.Key {
			old := c.f.Items[i]
			it.At, it.Read = old.At, old.Read
			c.f.Items[i] = it
			c.saveLocked()
			return
		}
	}
	if it.At == 0 {
		it.At = c.now().Unix()
	}
	c.f.Items = append(c.f.Items, it)
	if len(c.f.Items) > maxItems { // выбрасываем самые старые
		sort.SliceStable(c.f.Items, func(i, j int) bool { return c.f.Items[i].At < c.f.Items[j].At })
		c.f.Items = c.f.Items[len(c.f.Items)-maxItems:]
	}
	c.saveLocked()
}

// Resolve удаляет уведомления вида kind, ключей которых нет в keep (обновление уже установлено — напоминание не нужно).
func (c *Center) Resolve(kind string, keep map[string]bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.f.Items[:0]
	changed := false
	for _, it := range c.f.Items {
		if it.Kind == kind && !keep[it.Key] {
			changed = true
			continue
		}
		out = append(out, it)
	}
	c.f.Items = out
	if changed {
		c.saveLocked()
	}
}

// List — уведомления, новые первыми.
func (c *Center) List() []Item {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := append([]Item{}, c.f.Items...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].At > out[j].At })
	return out
}

// Unread — число непрочитанных.
func (c *Center) Unread() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, it := range c.f.Items {
		if !it.Read {
			n++
		}
	}
	return n
}

// MarkRead отмечает прочитанным уведомление с ключом key; пустой ключ — все.
func (c *Center) MarkRead(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.f.Items {
		if key == "" || c.f.Items[i].Key == key {
			c.f.Items[i].Read = true
		}
	}
	c.saveLocked()
}

// Dismiss удаляет уведомление; пустой ключ — все. Удалённое обновление появится снова только при следующей проверке,
// если оно всё ещё не установлено.
func (c *Center) Dismiss(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if key == "" {
		c.f.Items = nil
	} else {
		out := c.f.Items[:0]
		for _, it := range c.f.Items {
			if it.Key != key {
				out = append(out, it)
			}
		}
		c.f.Items = out
	}
	c.saveLocked()
}

// DismissPrefix удаляет уведомления, ключи которых начинаются с prefix (плагин установлен/обновлён — напоминание лишнее).
func (c *Center) DismissPrefix(prefix string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.f.Items[:0]
	for _, it := range c.f.Items {
		if !strings.HasPrefix(it.Key, prefix) {
			out = append(out, it)
		}
	}
	c.f.Items = out
	c.saveLocked()
}

// LastCheck — время последней попытки проверки (нулевое, если не было).
func (c *Center) LastCheck() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.f.LastCheck == 0 {
		return time.Time{}
	}
	return time.Unix(c.f.LastCheck, 0)
}

// Candidate — найденное обновление; Check превращает его в уведомление.
type Candidate struct {
	Key, Title, Text string
	Data             any
}

// Finder ищет обновления. ok=false — ответить было некому (нет сети): список не считается надёжным и ничего не снимает.
type Finder func(ctx context.Context) (list []Candidate, ok bool)

var (
	ErrTooSoon  = errors.New("check was run recently")
	ErrChecking = errors.New("check is already running")
)

// Check ищет обновления плагинов и обновляет уведомления вида KindPluginUpdate.
//
// Защита от лишних запросов: время попытки записывается на диск ДО поиска, поэтому при падении приложения посреди проверки
// повтор не уйдёт раньше срока. force=false (запуск приложения): не раньше CheckInterval после прошлой попытки
// (FailedInterval, если она не удалась); force=true (кнопка): не раньше ManualInterval.
func (c *Center) Check(ctx context.Context, force bool, find Finder) error {
	if !c.checkMu.TryLock() {
		return ErrChecking
	}
	defer c.checkMu.Unlock()

	c.mu.Lock()
	last := time.Unix(c.f.LastCheck, 0)
	wait := CheckInterval
	switch {
	case force:
		wait = ManualInterval
	case c.f.LastCheckFail:
		wait = FailedInterval
	}
	if c.f.LastCheck != 0 {
		if age := c.now().Sub(last); age < wait && age > -time.Minute {
			c.mu.Unlock()
			return ErrTooSoon
		}
	}
	c.f.LastCheck, c.f.LastCheckFail = c.now().Unix(), true
	c.saveLocked()
	c.mu.Unlock()

	list, ok := find(ctx)
	if !ok {
		return errors.New("no update source answered")
	}
	keep := map[string]bool{}
	for _, cand := range list {
		keep[cand.Key] = true
		raw, _ := json.Marshal(cand.Data)
		c.Upsert(Item{Key: cand.Key, Kind: KindPluginUpdate, Title: cand.Title, Text: cand.Text, Data: raw})
	}
	c.Resolve(KindPluginUpdate, keep)
	c.mu.Lock()
	c.f.LastCheckFail = false
	c.saveLocked()
	c.mu.Unlock()
	return nil
}
