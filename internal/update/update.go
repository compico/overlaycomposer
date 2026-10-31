// Package update проверяет, вышла ли новая версия приложения на GitHub Releases.
//
// Проверка работает только в релизных сборках (см. Enabled) и бережёт лимит GitHub API (60 запросов/час с одного IP):
// время последней попытки записывается на диск ДО запроса и учитывается между запусками, поэтому даже при crashloop
// приложения запрос уходит не чаще одного раза в MinInterval. Ошибки (нет сети, лимит) тоже считаются попыткой.
package update

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// MinInterval — минимальный промежуток между сетевыми запросами (между запусками тоже).
	MinInterval = 6 * time.Hour
	// ErrorInterval — после неудачи пробуем не раньше, чем через этот срок.
	ErrorInterval = time.Hour

	DefaultAPI = "https://api.github.com/repos/compico/overlaycomposer/releases/latest"
)

// Версия релиза: vГГГГ.ММ.ДД.N (см. .github/workflows/release.yml).
var versionRe = regexp.MustCompile(`^v(\d{4})\.(\d{2})\.(\d{2})\.(\d+)$`)

// Enabled — проверки включены: сборка помечена как релизная и версия имеет вид релизного тега.
// Локальные сборки (go run, make, git describe вида v…-3-gabc, dev) не обновляются.
func Enabled(channel, version string) bool {
	return channel == "release" && versionRe.MatchString(version)
}

// Info — сведения о последнем известном релизе.
type Info struct {
	Latest    string `json:"latest"` // тег, например v2026.10.27.5
	URL       string `json:"url"`    // страница релиза
	Notes     string `json:"notes"`  // описание релиза (markdown)
	Available bool   `json:"available"`
}

type state struct {
	CheckedAt time.Time `json:"checkedAt"`
	Failed    bool      `json:"failed,omitempty"`
	Latest    string    `json:"latest,omitempty"`
	URL       string    `json:"url,omitempty"`
	Notes     string    `json:"notes,omitempty"`
}

type Checker struct {
	Current   string
	StateFile string
	API       string // по умолчанию DefaultAPI
	Client    *http.Client
	Now       func() time.Time

	mu   sync.Mutex
	info Info
}

// Info возвращает последний известный результат (без сети).
func (c *Checker) Info() Info {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.info
}

// Check возвращает результат, ходя в сеть только если с прошлой попытки прошло достаточно времени.
func (c *Checker) Check(ctx context.Context) (Info, error) {
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	st := c.load()
	wait := MinInterval
	if st.Failed {
		wait = ErrorInterval
	}
	if !st.CheckedAt.IsZero() && now().Sub(st.CheckedAt) < wait && !st.CheckedAt.After(now().Add(time.Minute)) {
		return c.set(st), nil // кеш
	}

	// Сначала фиксируем попытку на диске: если приложение упадёт посреди запроса, повтор не уйдёт раньше срока.
	next := state{CheckedAt: now(), Failed: true, Latest: st.Latest, URL: st.URL, Notes: st.Notes}
	c.save(next)

	rel, err := c.fetch(ctx)
	if err != nil {
		return c.set(next), err
	}
	next = state{CheckedAt: next.CheckedAt, Latest: rel.Tag, URL: rel.URL, Notes: rel.Body}
	c.save(next)
	return c.set(next), nil
}

func (c *Checker) set(st state) Info {
	i := Info{Latest: st.Latest, URL: st.URL, Notes: st.Notes, Available: Newer(st.Latest, c.Current)}
	c.mu.Lock()
	c.info = i
	c.mu.Unlock()
	return i
}

type release struct {
	Tag  string `json:"tag_name"`
	URL  string `json:"html_url"`
	Body string `json:"body"`
}

func (c *Checker) fetch(ctx context.Context) (release, error) {
	var rel release
	api := c.API
	if api == "" {
		api = DefaultAPI
	}
	cl := c.Client
	if cl == nil {
		cl = &http.Client{Timeout: 10 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, api, nil)
	if err != nil {
		return rel, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "overlay-composer/"+c.Current)
	resp, err := cl.Do(req)
	if err != nil {
		return rel, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return rel, fmt.Errorf("github: HTTP %d", resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&rel); err != nil {
		return rel, err
	}
	if !versionRe.MatchString(rel.Tag) {
		return rel, fmt.Errorf("github: неожиданный тег %q", rel.Tag)
	}
	if !strings.HasPrefix(rel.URL, "https://github.com/") {
		rel.URL = ""
	}
	if len(rel.Body) > 20000 {
		rel.Body = rel.Body[:20000]
	}
	return rel, nil
}

func (c *Checker) load() state {
	var st state
	if raw, err := os.ReadFile(c.StateFile); err == nil {
		_ = json.Unmarshal(raw, &st)
	}
	return st
}

func (c *Checker) save(st state) {
	raw, _ := json.Marshal(st)
	_ = os.WriteFile(c.StateFile, raw, 0o600)
}

// Newer — latest строго новее current (оба вида vГГГГ.ММ.ДД.N); при непонятном формате — false.
func Newer(latest, current string) bool {
	a, b := parse(latest), parse(current)
	if a == nil || b == nil {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return false
}

func parse(v string) []int {
	m := versionRe.FindStringSubmatch(v)
	if m == nil {
		return nil
	}
	out := make([]int, 4)
	for i := range out {
		out[i], _ = strconv.Atoi(m[i+1])
	}
	return out
}
