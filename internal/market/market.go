// Package market — магазин плагинов: плагины лежат в репозиториях GitHub (папка plugins/<id>/ с plugin.json),
// пользователь добавляет репозитории-источники, а OC показывает каталог и ставит/обновляет/удаляет плагины
// в папке пользовательских плагинов.
//
// Как это работает: репозиторий скачивается одним zip-архивом с codeload.github.com (без GitHub API и его лимитов),
// архив читается в памяти и проверяется, на диск попадает только папка выбранного плагина. Плагин — чужой код,
// поэтому OC ничего не доверяет автоматически: ставит только по явной команде пользователя, не перезаписывает
// чужие и встроенные плагины и не запускает ничего при установке (плагин — это файлы, работает он уже в sandbox-iframe
// с разрешениями, которые выдаёт пользователь).
package market

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/compico/overlaycomposer/internal/model"
	"github.com/compico/overlaycomposer/internal/plugins"
)

// DefaultSource — официальный репозиторий плагинов; кладётся в список при первом запуске, потом его можно убрать.
const DefaultSource = "compico/oc-plugins"

const (
	maxZip      = 64 << 20 // размер скачиваемого архива
	maxFile     = 16 << 20 // один файл плагина
	maxTotal    = 64 << 20 // все файлы одного плагина
	maxEntries  = 5000     // записей в архиве
	catalogTTL  = 10 * time.Minute
	fetchTimout = 30 * time.Second
	pluginsRoot = "plugins" // папка с плагинами внутри репозитория
)

// Ошибки, которые сервер показывает пользователю как есть.
var (
	ErrBadRepo  = errors.New("repository must look like owner/repo or owner/repo@branch")
	ErrNoSource = errors.New("source is not in the list")
	ErrNoPlugin = errors.New("plugin not found in that repository")
	ErrConflict = errors.New("a plugin with this id already exists and was not installed from the store")
	ErrNotOurs  = errors.New("plugin was not installed from the store (remove its folder by hand)")
)

// Repo — репозиторий GitHub и необязательная ветка/тег/коммит.
type Repo struct {
	Owner, Name, Ref string
}

var (
	ownerRe = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)
	nameRe  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
	refRe   = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,100}$`)
)

// ParseRepo принимает owner/repo, owner/repo@ref и ссылки https://github.com/owner/repo[.git][/tree/ref].
func ParseRepo(s string) (Repo, error) {
	s = strings.TrimSpace(s)
	for _, p := range []string{"https://github.com/", "http://github.com/", "github.com/"} {
		if strings.HasPrefix(strings.ToLower(s), p) {
			s = s[len(p):]
			break
		}
	}
	var r Repo
	if i := strings.Index(s, "@"); i >= 0 {
		r.Ref, s = s[i+1:], s[:i]
		if r.Ref == "" {
			return Repo{}, ErrBadRepo
		}
	}
	parts := strings.Split(strings.Trim(s, "/"), "/")
	if len(parts) >= 4 && parts[2] == "tree" && r.Ref == "" { // …/owner/repo/tree/<ref>
		r.Ref = strings.Join(parts[3:], "/")
		parts = parts[:2]
	}
	if len(parts) != 2 {
		return Repo{}, ErrBadRepo
	}
	r.Owner, r.Name = parts[0], strings.TrimSuffix(parts[1], ".git")
	if !ownerRe.MatchString(r.Owner) || !nameRe.MatchString(r.Name) || r.Name == "." || r.Name == ".." {
		return Repo{}, ErrBadRepo
	}
	if r.Ref != "" && (!refRe.MatchString(r.Ref) || strings.Contains(r.Ref, "..") || strings.HasPrefix(r.Ref, "/") || strings.HasSuffix(r.Ref, "/")) {
		return Repo{}, ErrBadRepo
	}
	return r, nil
}

// String — каноничная запись (то, что хранится и показывается).
func (r Repo) String() string {
	s := r.Owner + "/" + r.Name
	if r.Ref != "" {
		s += "@" + r.Ref
	}
	return s
}

// Fetcher скачивает архив репозитория (zip). Подменяется в тестах.
type Fetcher func(ctx context.Context, r Repo) ([]byte, error)

// HTTPFetcher качает с codeload.github.com (base можно заменить для тестов).
func HTTPFetcher(base string) Fetcher {
	if base == "" {
		base = "https://codeload.github.com"
	}
	client := &http.Client{Timeout: fetchTimout}
	return func(ctx context.Context, r Repo) ([]byte, error) {
		ref := r.Ref
		if ref == "" {
			ref = "HEAD"
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/"+r.Owner+"/"+r.Name+"/zip/"+ref, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", "overlay-composer")
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("cannot reach GitHub: %w", err)
		}
		defer resp.Body.Close()
		switch {
		case resp.StatusCode == http.StatusNotFound:
			return nil, errors.New("repository or branch not found (is it public?)")
		case resp.StatusCode != http.StatusOK:
			return nil, fmt.Errorf("GitHub answered %s", resp.Status)
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, maxZip+1))
		if err != nil {
			return nil, err
		}
		if len(b) > maxZip {
			return nil, errors.New("repository archive is too large")
		}
		return b, nil
	}
}

// Entry — плагин из каталога репозитория.
type Entry struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Version     string   `json:"version,omitempty"`
	Description string   `json:"description,omitempty"`
	Author      string   `json:"author,omitempty"`
	Permissions []string `json:"permissions,omitempty"`
	Repo        string   `json:"repo"`

	Installed        bool   `json:"installed"`                  // установлен из этого репозитория
	InstalledVersion string `json:"installedVersion,omitempty"` //
	UpdateAvailable  bool   `json:"updateAvailable"`            //
	Blocked          string `json:"blocked,omitempty"`          // почему нельзя поставить: "builtin" | "foreign"
}

// SourceView — источник и его каталог (или ошибка загрузки).
type SourceView struct {
	Repo    string  `json:"repo"`
	Error   string  `json:"error,omitempty"`
	Plugins []Entry `json:"plugins"`
}

// Record — установленный из магазина плагин.
type Record struct {
	Repo    string `json:"repo"`
	Version string `json:"version,omitempty"`
	At      int64  `json:"at"`
}

type state struct {
	Sources   []string          `json:"sources"`
	Installed map[string]Record `json:"installed"`
}

// Market — состояние магазина (список источников, установленные плагины) и операции над ним.
type Market struct {
	file    string // market.json; "" — только в памяти
	dir     string // папка пользовательских плагинов
	reg     *plugins.Registry
	fetch   Fetcher
	builtin func(id string) bool

	opMu sync.Mutex // одна операция установки/удаления за раз
	mu   sync.Mutex
	st   state

	cacheMu sync.Mutex
	cache   map[string]cached
}

type cached struct {
	at      time.Time
	plugins []parsed
}

// Config — зависимости Market.
type Config struct {
	File    string
	Dir     string
	Reg     *plugins.Registry
	Fetch   Fetcher
	Builtin func(id string) bool // id занят встроенным плагином
}

// New читает состояние; при первом запуске (файла нет) добавляет официальный источник.
func New(c Config) (*Market, error) {
	if c.Fetch == nil {
		c.Fetch = HTTPFetcher("")
	}
	if c.Builtin == nil {
		c.Builtin = func(string) bool { return false }
	}
	m := &Market{file: c.File, dir: c.Dir, reg: c.Reg, fetch: c.Fetch, builtin: c.Builtin, cache: map[string]cached{}}
	m.st.Installed = map[string]Record{}
	if c.File != "" {
		raw, err := os.ReadFile(c.File)
		switch {
		case err == nil:
			if err := json.Unmarshal(raw, &m.st); err != nil {
				return nil, fmt.Errorf("market: %s: %w", c.File, err)
			}
			if m.st.Installed == nil {
				m.st.Installed = map[string]Record{}
			}
		case errors.Is(err, fs.ErrNotExist):
			m.st.Sources = []string{DefaultSource}
			if err := m.saveLocked(); err != nil {
				return nil, err
			}
		default:
			return nil, err
		}
	}
	return m, nil
}

func (m *Market) saveLocked() error {
	if m.file == "" {
		return nil
	}
	raw, err := json.MarshalIndent(m.st, "", "  ")
	if err != nil {
		return err
	}
	tmp := m.file + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, m.file)
}

// Sources — текущий список источников.
func (m *Market) Sources() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string{}, m.st.Sources...)
}

// AddSource проверяет, что репозиторий скачивается и в нём есть плагины, и добавляет его в список.
func (m *Market) AddSource(ctx context.Context, in string) (string, error) {
	r, err := ParseRepo(in)
	if err != nil {
		return "", err
	}
	key := r.String()
	m.mu.Lock()
	for _, s := range m.st.Sources {
		if strings.EqualFold(s, key) {
			m.mu.Unlock()
			return s, nil
		}
	}
	m.mu.Unlock()
	list, err := m.load(ctx, r, true)
	if err != nil {
		return "", err
	}
	if len(list) == 0 {
		return "", errors.New("no plugins found: expected folders plugins/<id>/plugin.json in the repository")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.st.Sources = append(m.st.Sources, key)
	if err := m.saveLocked(); err != nil {
		m.st.Sources = m.st.Sources[:len(m.st.Sources)-1]
		return "", err
	}
	return key, nil
}

// RemoveSource убирает источник из списка. Уже установленные из него плагины остаются на месте.
func (m *Market) RemoveSource(in string) error {
	r, err := ParseRepo(in)
	if err != nil {
		return err
	}
	key := r.String()
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, s := range m.st.Sources {
		if strings.EqualFold(s, key) {
			old := m.st.Sources
			m.st.Sources = append(append([]string{}, old[:i]...), old[i+1:]...)
			if err := m.saveLocked(); err != nil {
				m.st.Sources = old
				return err
			}
			m.cacheMu.Lock()
			delete(m.cache, strings.ToLower(key))
			m.cacheMu.Unlock()
			return nil
		}
	}
	return ErrNoSource
}

// Catalog — каталоги всех источников (параллельно; ошибка одного источника не мешает остальным).
func (m *Market) Catalog(ctx context.Context, refresh bool) []SourceView {
	srcs := m.Sources()
	out := make([]SourceView, len(srcs))
	var wg sync.WaitGroup
	for i, s := range srcs {
		wg.Add(1)
		go func(i int, s string) {
			defer wg.Done()
			v := SourceView{Repo: s, Plugins: []Entry{}}
			r, err := ParseRepo(s)
			if err == nil {
				var list []parsed
				if list, err = m.load(ctx, r, refresh); err == nil {
					v.Plugins = m.entries(s, list)
				}
			}
			if err != nil {
				v.Error = err.Error()
			}
			out[i] = v
		}(i, s)
	}
	wg.Wait()
	return out
}

func (m *Market) entries(repo string, list []parsed) []Entry {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Entry, 0, len(list))
	for _, p := range list {
		e := Entry{ID: p.m.ID, Name: p.m.Name, Version: p.m.Version, Description: p.m.Description, Author: p.m.Author, Permissions: p.m.Permissions, Repo: repo}
		rec, ours := m.st.Installed[p.m.ID]
		switch {
		case m.builtin(p.m.ID):
			e.Blocked = "builtin"
		case ours && strings.EqualFold(rec.Repo, repo):
			e.Installed, e.InstalledVersion = true, rec.Version
			e.UpdateAvailable = newer(p.m.Version, rec.Version)
		case ours:
			e.Blocked = "foreign" // уже стоит из другого репозитория
		case m.existsOnDisk(p.m.ID):
			e.Blocked = "foreign" // положен руками
		}
		out = append(out, e)
	}
	return out
}

func (m *Market) existsOnDisk(id string) bool {
	_, err := os.Stat(filepath.Join(m.dir, id))
	return err == nil
}

// Installed — id плагинов, установленных из магазина, и их записи.
func (m *Market) Installed() map[string]Record {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]Record, len(m.st.Installed))
	for k, v := range m.st.Installed {
		out[k] = v
	}
	return out
}

// newer: версия из каталога новее установленной (числа через точку; иначе — просто «не равно»).
func newer(avail, have string) bool {
	if avail == have || avail == "" {
		return false
	}
	a, aok := splitVer(avail)
	h, hok := splitVer(have)
	if !aok || !hok {
		return true
	}
	for i := 0; i < len(a) || i < len(h); i++ {
		var x, y int
		if i < len(a) {
			x = a[i]
		}
		if i < len(h) {
			y = h[i]
		}
		if x != y {
			return x > y
		}
	}
	return false
}

func splitVer(s string) ([]int, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	if s == "" {
		return nil, false
	}
	var out []int
	for _, p := range strings.Split(s, ".") {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return nil, false
		}
		out = append(out, n)
	}
	return out, true
}

// ---- архив ----

// parsed — плагин, найденный в архиве: манифест и файлы (путь внутри папки плагина → запись zip).
type parsed struct {
	m     plugins.Manifest
	files map[string]*zip.File
}

// load возвращает плагины репозитория (с кэшем на catalogTTL).
func (m *Market) load(ctx context.Context, r Repo, refresh bool) ([]parsed, error) {
	key := strings.ToLower(r.String())
	if !refresh {
		m.cacheMu.Lock()
		c, ok := m.cache[key]
		m.cacheMu.Unlock()
		if ok && time.Since(c.at) < catalogTTL {
			return c.plugins, nil
		}
	}
	data, err := m.fetch(ctx, r)
	if err != nil {
		return nil, err
	}
	list, err := parseZip(data)
	if err != nil {
		return nil, err
	}
	m.cacheMu.Lock()
	m.cache[key] = cached{at: time.Now(), plugins: list}
	m.cacheMu.Unlock()
	return list, nil
}

// parseZip находит в архиве <корень>/plugins/<id>/plugin.json; битые плагины пропускаются.
func parseZip(data []byte) ([]parsed, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("bad archive: %w", err)
	}
	if len(zr.File) > maxEntries {
		return nil, errors.New("archive has too many files")
	}
	byID := map[string]*parsed{}
	var order []string
	for _, f := range zr.File {
		if f.FileInfo().IsDir() || !f.Mode().IsRegular() {
			continue // каталоги, симлинки и прочее — не берём
		}
		parts := strings.Split(f.Name, "/")
		// parts[0] — корень архива (<repo>-<ref>), затем plugins/<id>/…файл
		if len(parts) < 4 || parts[1] != pluginsRoot || !fs.ValidPath(f.Name) {
			continue
		}
		id := parts[2]
		p := byID[id]
		if p == nil {
			p = &parsed{files: map[string]*zip.File{}}
			byID[id] = p
			order = append(order, id)
		}
		p.files[strings.Join(parts[3:], "/")] = f
	}
	var out []parsed
	sort.Strings(order)
	for _, id := range order {
		p := byID[id]
		mf := p.files[plugins.ManifestFile]
		if mf == nil || mf.UncompressedSize64 > 1<<20 {
			continue
		}
		raw, err := readZip(mf, 1<<20)
		if err != nil {
			continue
		}
		var man plugins.Manifest
		if json.Unmarshal(raw, &man) != nil || man.Validate() != nil || man.ID != id || p.files[man.Entry] == nil {
			continue
		}
		man.Builtin = false
		p.m = man
		out = append(out, *p)
	}
	return out, nil
}

func readZip(f *zip.File, limit int64) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, errors.New("file is too large")
	}
	return b, nil
}

// ---- установка и удаление ----

// Install ставит (или обновляет) плагин id из репозитория repo. Скачивает архив заново, чтобы поставить ровно то, что
// сейчас в репозитории; на диск попадает только папка этого плагина.
func (m *Market) Install(ctx context.Context, repoIn, id string) (Entry, error) {
	r, err := ParseRepo(repoIn)
	if err != nil {
		return Entry{}, err
	}
	key := r.String()
	m.opMu.Lock()
	defer m.opMu.Unlock()

	m.mu.Lock()
	listed := false
	for _, s := range m.st.Sources {
		if strings.EqualFold(s, key) {
			key, listed = s, true
		}
	}
	rec, ours := m.st.Installed[id]
	m.mu.Unlock()
	if !listed {
		return Entry{}, ErrNoSource
	}
	if m.builtin(id) {
		return Entry{}, ErrConflict
	}
	dst := filepath.Join(m.dir, id)
	_, statErr := os.Stat(dst)
	exists := statErr == nil
	if (exists && !ours) || (ours && !strings.EqualFold(rec.Repo, key)) {
		return Entry{}, ErrConflict
	}

	list, err := m.load(ctx, r, true)
	if err != nil {
		return Entry{}, err
	}
	var p *parsed
	for i := range list {
		if list[i].m.ID == id {
			p = &list[i]
		}
	}
	if p == nil {
		return Entry{}, ErrNoPlugin
	}

	tmp, err := os.MkdirTemp(m.dir, ".install-"+id+"-")
	if err != nil {
		return Entry{}, err
	}
	defer os.RemoveAll(tmp)
	if err := extract(p, tmp); err != nil {
		return Entry{}, err
	}
	if exists {
		old, err := os.MkdirTemp(m.dir, ".old-"+id+"-")
		if err != nil {
			return Entry{}, err
		}
		defer os.RemoveAll(old)
		bak := filepath.Join(old, id)
		if err := os.Rename(dst, bak); err != nil {
			return Entry{}, err
		}
		if err := os.Rename(tmp, dst); err != nil {
			_ = os.Rename(bak, dst) // вернуть как было
			return Entry{}, err
		}
	} else if err := os.Rename(tmp, dst); err != nil {
		return Entry{}, err
	}

	m.mu.Lock()
	m.st.Installed[id] = Record{Repo: key, Version: p.m.Version, At: time.Now().Unix()}
	err = m.saveLocked()
	m.mu.Unlock()
	if m.reg != nil {
		m.reg.Scan()
	}
	if err != nil {
		return Entry{}, err
	}
	return m.entries(key, []parsed{*p})[0], nil
}

// extract записывает файлы плагина в dir (пути уже проверены fs.ValidPath; размеры ограничены).
func extract(p *parsed, dir string) error {
	var total int64
	names := make([]string, 0, len(p.files))
	for n := range p.files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if !fs.ValidPath(n) {
			return fmt.Errorf("bad path %q", n)
		}
		f := p.files[n]
		if f.UncompressedSize64 > maxFile {
			return fmt.Errorf("%s is too large", n)
		}
		b, err := readZip(f, maxFile)
		if err != nil {
			return fmt.Errorf("%s: %w", n, err)
		}
		if total += int64(len(b)); total > maxTotal {
			return errors.New("plugin is too large")
		}
		out := filepath.Join(dir, filepath.FromSlash(n))
		if rel, err := filepath.Rel(dir, out); err != nil || strings.HasPrefix(rel, "..") {
			return fmt.Errorf("bad path %q", n)
		}
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(out, b, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// Uninstall удаляет плагин, поставленный из магазина (чужие и ручные плагины не трогает).
func (m *Market) Uninstall(id string) error {
	if !isID(id) {
		return ErrNotOurs
	}
	m.opMu.Lock()
	defer m.opMu.Unlock()
	m.mu.Lock()
	rec, ours := m.st.Installed[id]
	m.mu.Unlock()
	if !ours {
		return ErrNotOurs
	}
	if err := os.RemoveAll(filepath.Join(m.dir, id)); err != nil {
		return err
	}
	m.mu.Lock()
	delete(m.st.Installed, id)
	err := m.saveLocked()
	if err != nil {
		m.st.Installed[id] = rec
	}
	m.mu.Unlock()
	if m.reg != nil {
		m.reg.Scan()
	}
	return err
}

func isID(s string) bool { return model.IsValidID(s) }
