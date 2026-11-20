package market

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Каталог автономных (wasm) плагинов. Они выпускаются в Releases репозитория-источника: тег <id>-v<версия>, ассеты
// <id>.wasm, <id>.wasm.sha256 и <id>.json (id, version, sha256, size, meta — по нему строится каталог, плагин для этого не скачивается).
// Здесь только чтение: установка и учёт версий — отдельно (см. #176).

const (
	maxWasmInfo     = 256 << 10 // <id>.json
	maxWasmSize     = 64 << 20  // размер .wasm, который мы согласны показать в каталоге
	maxWasmPlugins  = 64        // плагинов в одном источнике
	maxReleasePages = 5         // страниц по 100 релизов
)

var wasmTagRe = regexp.MustCompile(`^([a-z0-9][a-z0-9_-]{0,31})-v(\d+\.\d+\.\d+)$`)
var sha256Re = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ReleaseAsset — файл релиза.
type ReleaseAsset struct {
	ID   int64
	Name string
	Size int64
}

// Release — релиз GitHub.
type Release struct {
	Tag        string
	Draft      bool
	Prerelease bool
	Assets     []ReleaseAsset
}

// ReleaseSource — доступ к релизам репозитория (в тестах подменяется).
type ReleaseSource interface {
	// List возвращает релизы репозитория, новые первыми.
	List(ctx context.Context, r Repo, token string) ([]Release, error)
	// Asset скачивает файл релиза; больше max байт — ошибка.
	Asset(ctx context.Context, r Repo, token string, id int64, max int64) ([]byte, error)
}

type httpReleases struct {
	api    string
	client *http.Client
}

// HTTPReleases — релизы через GitHub API (apiBase пустой — https://api.github.com). Токен уходит только на этот адрес: при
// редиректе на подписанный адрес скачивания заголовок Authorization снимается.
func HTTPReleases(apiBase string) ReleaseSource {
	if apiBase == "" {
		apiBase = "https://api.github.com"
	}
	return &httpReleases{api: strings.TrimRight(apiBase, "/"), client: &http.Client{
		Timeout: fetchTimout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			if req.URL.Scheme != via[0].URL.Scheme {
				return errors.New("redirect to another scheme refused")
			}
			if req.URL.Host != via[0].URL.Host {
				req.Header.Del("Authorization")
			}
			return nil
		},
	}}
}

func (h *httpReleases) get(ctx context.Context, url, token, accept string, max int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "overlay-composer")
	req.Header.Set("Accept", accept)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cannot reach GitHub: %w", err) // URL в тексте есть, токена нет
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound && token == "":
		return nil, errors.New("repository not found (is it public? for a private repository add a GitHub token)")
	case resp.StatusCode == http.StatusNotFound:
		return nil, errors.New("repository not found, or the token has no access to it")
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		if token != "" {
			return nil, errors.New("GitHub rejected the token (expired, no access to this repository, or rate limit)")
		}
		return nil, errors.New("GitHub refused the request (rate limit? add a GitHub token in the store settings)")
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("GitHub answered %s", resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, errors.New("GitHub answer is too large")
	}
	return b, nil
}

func (h *httpReleases) List(ctx context.Context, r Repo, token string) ([]Release, error) {
	var out []Release
	for page := 1; page <= maxReleasePages; page++ {
		url := fmt.Sprintf("%s/repos/%s/%s/releases?per_page=100&page=%d", h.api, r.Owner, r.Name, page)
		raw, err := h.get(ctx, url, token, "application/vnd.github+json", 8<<20)
		if err != nil {
			return nil, err
		}
		var list []struct {
			Tag        string `json:"tag_name"`
			Draft      bool   `json:"draft"`
			Prerelease bool   `json:"prerelease"`
			Assets     []struct {
				ID   int64  `json:"id"`
				Name string `json:"name"`
				Size int64  `json:"size"`
			} `json:"assets"`
		}
		if err := json.Unmarshal(raw, &list); err != nil {
			return nil, errors.New("GitHub sent an unreadable release list")
		}
		for _, x := range list {
			rel := Release{Tag: x.Tag, Draft: x.Draft, Prerelease: x.Prerelease}
			for _, a := range x.Assets {
				rel.Assets = append(rel.Assets, ReleaseAsset{ID: a.ID, Name: a.Name, Size: a.Size})
			}
			out = append(out, rel)
		}
		if len(list) < 100 {
			break
		}
	}
	return out, nil
}

func (h *httpReleases) Asset(ctx context.Context, r Repo, token string, id int64, max int64) ([]byte, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/releases/assets/%d", h.api, r.Owner, r.Name, id)
	return h.get(ctx, url, token, "application/octet-stream", max)
}

// WasmVersion — выпущенная версия плагина.
type WasmVersion struct {
	Version string `json:"version"`
	Tag     string `json:"tag"`
}

// WasmEntry — wasm-плагин из релизов источника (последняя версия и список всех).
type WasmEntry struct {
	ID          string        `json:"id"`
	Name        string        `json:"name"`
	Version     string        `json:"version"`
	Description string        `json:"description,omitempty"`
	Author      string        `json:"author,omitempty"`
	Permissions []string      `json:"permissions,omitempty"`
	Repo        string        `json:"repo"`
	Tag         string        `json:"tag"`
	Sha256      string        `json:"sha256,omitempty"`
	Size        int64         `json:"size,omitempty"`
	NoInfo      bool          `json:"noInfo,omitempty"` // у последнего релиза нет <id>.json: имя, права и sha256 неизвестны
	Versions    []WasmVersion `json:"versions"`         // от новой к старой
	// Changelog — журнал изменений из <id>.changelog.json последнего релиза (вся история). Отдаётся только для установленных плагинов:
	// редактор показывает из него записи пропущенных версий (между установленной и выбранной).
	Changelog []Change `json:"changelog,omitempty"`

	Installed        bool     `json:"installed"`                  // установлен из этого репозитория через магазин
	InstalledVersion string   `json:"installedVersion,omitempty"` //
	InstalledTag     string   `json:"installedTag,omitempty"`     //
	UpdateAvailable  bool     `json:"updateAvailable"`            // в каталоге версия новее установленной
	AddedPermissions []string `json:"addedPermissions,omitempty"` // права последней версии, которых нет у установленной
	Blocked          string   `json:"blocked,omitempty"`          // "foreign": плагин с таким id уже стоит, но не из магазина (или из другого источника)
}

// WasmSource — источник и его wasm-плагины (или ошибка загрузки).
type WasmSource struct {
	Repo    string      `json:"repo"`
	Error   string      `json:"error,omitempty"`
	Plugins []WasmEntry `json:"plugins"`
}

type wasmCached struct {
	at      time.Time
	plugins []WasmEntry
}

type wasmCache struct {
	mu sync.Mutex
	m  map[string]wasmCached
}

// WasmCatalog — wasm-плагины всех источников. Источник без таких релизов даёт пустой список, не ошибку.
func (m *Market) WasmCatalog(ctx context.Context, refresh bool) []WasmSource {
	var out []WasmSource
	for _, src := range m.Sources() {
		sv := WasmSource{Repo: src, Plugins: []WasmEntry{}}
		r, err := ParseRepo(src)
		if err != nil {
			sv.Error = err.Error()
			out = append(out, sv)
			continue
		}
		list, err := m.wasmLoad(ctx, r, src, refresh)
		if err != nil {
			sv.Error = err.Error()
		} else if list != nil {
			sv.Plugins = m.markWasmInstalled(src, list)
		}
		out = append(out, sv)
	}
	return out
}

func (m *Market) wasmLoad(ctx context.Context, r Repo, source string, refresh bool) ([]WasmEntry, error) {
	key := strings.ToLower(r.Owner + "/" + r.Name)
	if !refresh {
		m.wasm.mu.Lock()
		c, ok := m.wasm.m[key]
		m.wasm.mu.Unlock()
		if ok && time.Since(c.at) < catalogTTL {
			return c.plugins, nil
		}
	}
	list, err := m.rel.List(ctx, r, m.token())
	if err != nil {
		return nil, err
	}
	entries := m.wasmEntries(ctx, r, source, list)
	m.wasm.mu.Lock()
	if m.wasm.m == nil {
		m.wasm.m = map[string]wasmCached{}
	}
	m.wasm.m[key] = wasmCached{at: time.Now(), plugins: entries}
	m.wasm.mu.Unlock()
	return entries, nil
}

// wasmEntries группирует релизы по плагинам и читает <id>.json последней версии каждого.
func (m *Market) wasmEntries(ctx context.Context, r Repo, source string, releases []Release) []WasmEntry {
	type ver struct {
		v   string
		rel Release
	}
	byID := map[string][]ver{}
	for _, rel := range releases {
		if rel.Draft || rel.Prerelease {
			continue
		}
		g := wasmTagRe.FindStringSubmatch(rel.Tag)
		if g == nil {
			continue
		}
		dup := false
		for _, x := range byID[g[1]] {
			dup = dup || x.v == g[2]
		}
		if !dup {
			byID[g[1]] = append(byID[g[1]], ver{g[2], rel})
		}
	}
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if len(ids) > maxWasmPlugins {
		ids = ids[:maxWasmPlugins]
	}
	out := []WasmEntry{}
	for _, id := range ids {
		vs := byID[id]
		sort.SliceStable(vs, func(i, j int) bool { return newer(vs[i].v, vs[j].v) })
		e := WasmEntry{ID: id, Name: id, Version: vs[0].v, Repo: source, Tag: vs[0].rel.Tag}
		for _, v := range vs {
			e.Versions = append(e.Versions, WasmVersion{Version: v.v, Tag: v.rel.Tag})
		}
		m.fillWasmInfo(ctx, r, &e, vs[0].rel)
		e.Changelog = m.readWasmChangelog(ctx, r, id, vs[0].rel)
		out = append(out, e)
	}
	return out
}

// wasmInfo — <id>.json релиза (его пишет scripts/wasm.mjs из oc-plugins).
type wasmInfo struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	Sha256  string `json:"sha256"`
	Size    int64  `json:"size"`
	Meta    struct {
		ID          string   `json:"id"`
		Name        string   `json:"name"`
		Version     string   `json:"version"`
		Description string   `json:"description"`
		Author      string   `json:"author"`
		Permissions []string `json:"permissions"`
	} `json:"meta"`
}

// readWasmInfo читает <id>.json релиза и возвращает его, только если он сходится с релизом (id, версия, размер .wasm).
// Нет файла или он не сходится — ok=false: ничего из чужого JSON не принимается на веру.
func (m *Market) readWasmInfo(ctx context.Context, r Repo, id, version string, rel Release) (in wasmInfo, ok bool) {
	var infoID, wasmSize int64
	for _, a := range rel.Assets {
		switch a.Name {
		case id + ".json":
			infoID = a.ID
		case id + ".wasm":
			wasmSize = a.Size
		}
	}
	if infoID == 0 || wasmSize == 0 {
		return in, false
	}
	raw, err := m.rel.Asset(ctx, r, m.token(), infoID, maxWasmInfo)
	if err != nil {
		return in, false
	}
	if json.Unmarshal(raw, &in) != nil || in.ID != id || in.Version != version || in.Meta.ID != id || in.Meta.Version != version ||
		!sha256Re.MatchString(in.Sha256) || in.Size <= 0 || in.Size > maxWasmSize || in.Size != wasmSize {
		return wasmInfo{}, false
	}
	return in, true
}

// fillWasmInfo дополняет запись данными из <id>.json релиза; без них запись остаётся без метаданных (NoInfo).
func (m *Market) fillWasmInfo(ctx context.Context, r Repo, e *WasmEntry, rel Release) {
	e.NoInfo = true
	in, ok := m.readWasmInfo(ctx, r, e.ID, e.Version, rel)
	if !ok {
		return
	}
	e.NoInfo = false
	e.Sha256, e.Size = in.Sha256, in.Size
	e.Name = oneLine(in.Meta.Name, 80, e.ID)
	e.Description = oneLine(in.Meta.Description, 500, "")
	e.Author = oneLine(in.Meta.Author, 80, "")
	for _, p := range in.Meta.Permissions {
		// Название права не обрезаем: длинное или с мусором — не право, а чужой текст; такое не показываем.
		if p != "" && len(p) <= 40 && p == oneLine(p, 40, "") && len(e.Permissions) < 32 {
			e.Permissions = append(e.Permissions, p)
		}
	}
}

// readWasmChangelog читает <id>.changelog.json релиза. Нет файла, он велик или битый — журнала просто нет: на установку это не влияет.
func (m *Market) readWasmChangelog(ctx context.Context, r Repo, id string, rel Release) []Change {
	for _, a := range rel.Assets {
		if a.Name != id+".changelog.json" || a.Size <= 0 || a.Size > maxChangelogSize {
			continue
		}
		raw, err := m.rel.Asset(ctx, r, m.token(), a.ID, maxChangelogSize)
		if err != nil {
			return nil
		}
		return parseChangelogBytes(raw)
	}
	return nil
}

// oneLine делает из чужого текста безопасную однострочную подпись: управляющие символы — в пробелы, длина — не больше max рун.
func sha256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func oneLine(s string, max int, def string) string {
	s = strings.Join(strings.FieldsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f }), " ")
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > max {
		s = string([]rune(s)[:max])
	}
	if s == "" {
		return def
	}
	return s
}
