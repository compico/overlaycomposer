package localnet

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Rule — одно разрешение манифеста плагина (поле netLocal): к какой программе на этом компьютере и к каким путям он
// может обращаться. Адрес задаётся либо прямо (Target), либо берётся из настройки виджета (Setting) — тогда порт выбирает
// пользователь в форме виджета, а не сам плагин.
type Rule struct {
	Target  string   `json:"target,omitempty"`  // "127.0.0.1:24050"
	Setting string   `json:"setting,omitempty"` // ключ настройки виджета со значением "host:port"
	Paths   []string `json:"paths"`             // допустимые префиксы пути: "/json/"
}

var (
	ErrNoRules    = errors.New("the plugin does not declare which local programs it uses (netLocal in plugin.json)")
	ErrNotAllowed = errors.New("this address or path is not allowed by the plugin manifest (netLocal)")
	ErrOwnPort    = errors.New("access to Overlay Composer itself is not allowed")
	ErrRate       = errors.New("too many local requests, slow down")
)

const (
	MaxRules     = 8
	MaxRulePaths = 16
)

// Validate проверяет правило при загрузке манифеста.
func (r Rule) Validate() error {
	if (r.Target == "") == (r.Setting == "") {
		return errors.New("netLocal: set exactly one of target / setting")
	}
	if r.Target != "" {
		if _, err := Target(r.Target); err != nil {
			return fmt.Errorf("netLocal: %w", err)
		}
	}
	if len(r.Paths) == 0 || len(r.Paths) > MaxRulePaths {
		return errors.New("netLocal: paths must list 1–16 path prefixes")
	}
	for _, p := range r.Paths {
		if err := checkPath(p); err != nil {
			return fmt.Errorf("netLocal: path %q: %w", p, err)
		}
	}
	return nil
}

// checkPath — путь без query, начинается с «/», без «..» (в том числе в %-кодировке), пробелов и обратных слэшей.
func checkPath(p string) error {
	if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || len(p) > 512 || strings.ContainsAny(p, " \r\n\t\\") {
		return ErrPath
	}
	raw := p
	if i := strings.IndexByte(raw, '?'); i >= 0 {
		raw = raw[:i]
	}
	dec, err := url.PathUnescape(raw)
	if err != nil {
		return ErrPath
	}
	for _, seg := range strings.Split(strings.ReplaceAll(dec, "\\", "/"), "/") {
		if seg == ".." {
			return ErrPath
		}
	}
	return nil
}

// Allowed проверяет запрос по правилам плагина. setting возвращает значение настройки виджета (с учётом умолчания).
// Возвращает адрес в нормализованном виде.
func Allowed(rules []Rule, setting func(key string) string, target, path string) (string, error) {
	if len(rules) == 0 {
		return "", ErrNoRules
	}
	t, err := Target(target)
	if err != nil {
		return "", err
	}
	if err := checkPath(path); err != nil {
		return "", err
	}
	pathOnly := path
	if i := strings.IndexByte(pathOnly, '?'); i >= 0 {
		pathOnly = pathOnly[:i]
	}
	for _, r := range rules {
		want := r.Target
		if r.Setting != "" {
			want = setting(r.Setting)
		}
		wt, err := Target(want)
		if err != nil || wt != t {
			continue
		}
		for _, prefix := range r.Paths {
			if strings.HasPrefix(pathOnly, prefix) {
				return t, nil
			}
		}
	}
	return "", ErrNotAllowed
}

// PortOf возвращает порт нормализованного адреса «host:port».
func PortOf(target string) string {
	i := strings.LastIndexByte(target, ':')
	if i < 0 {
		return ""
	}
	return target[i+1:]
}

// Limiter ограничивает запросы плагина к локальным программам: частоту (окно) и число одновременных запросов.
type Limiter struct {
	mu       sync.Mutex
	now      func() time.Time
	window   time.Duration
	max      int // запросов в окне на ключ
	parallel int // одновременных запросов на ключ
	total    int // одновременных запросов вообще
	entries  map[string]*limEntry
	inflight int
}

type limEntry struct {
	start    time.Time
	n        int
	inflight int
}

func NewLimiter(window time.Duration, max, parallel, total int) *Limiter {
	return &Limiter{now: time.Now, window: window, max: max, parallel: parallel, total: total, entries: map[string]*limEntry{}}
}

// Acquire занимает место для запроса ключа; done нужно вызвать по завершении запроса.
func (l *Limiter) Acquire(key string) (done func(), err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	e := l.entries[key]
	if e == nil {
		if len(l.entries) > 1000 {
			l.entries = map[string]*limEntry{}
		}
		e = &limEntry{start: now}
		l.entries[key] = e
	}
	if now.Sub(e.start) >= l.window {
		e.start, e.n = now, 0
	}
	if e.n >= l.max || e.inflight >= l.parallel || l.inflight >= l.total {
		return nil, ErrRate
	}
	e.n++
	e.inflight++
	l.inflight++
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			e.inflight--
			l.inflight--
			l.mu.Unlock()
		})
	}, nil
}
