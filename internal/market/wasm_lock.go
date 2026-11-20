package market

import (
	"context"
	"sort"
	"strings"
)

// Lock-файл wasm-плагинов: какие плагины, из какого источника, каким релизом и с каким sha256 стоят. Входит в экспорт настроек; при импорте
// ничего не ставится сразу — WasmLockPlan показывает по каждой записи, что с ней можно сделать и почему нельзя, а ставит пользователь
// выбранное (WasmInstall с тегом и sha256 из lock: версия ровно записанная, файл сверяется с lock, а не с текущим релизом).

// WasmLockEntry — запись lock-файла.
type WasmLockEntry struct {
	ID      string `json:"id"`
	Repo    string `json:"repo"`
	Tag     string `json:"tag"`
	Version string `json:"version"`
	Sha256  string `json:"sha256"`
	Size    int64  `json:"size,omitempty"`
}

// WasmLockFile — содержимое экспорта.
type WasmLockFile struct {
	Version int             `json:"version"`
	Plugins []WasmLockEntry `json:"plugins"`
}

// Статусы записи плана.
const (
	LockOK          = "ok"          // можно поставить записанную версию
	LockInstalled   = "installed"   // уже стоит ровно этот файл
	LockDiffers     = "differs"     // из магазина стоит другая версия или файл: можно вернуть записанный
	LockDeprecated  = "deprecated"  // плагин объявлен устаревшим
	LockRemoved     = "removed"     // релиз удалён из репозитория
	LockChanged     = "changed"     // файл релиза не совпадает с lock
	LockNoSource    = "no-source"   // источник не добавлен в магазин
	LockUnavailable = "unavailable" // источник не ответил
	LockConflict    = "conflict"    // id занят плагином, поставленным не из магазина (или из другого источника)
	LockInvalid     = "invalid"     // запись lock-файла негодная
)

// WasmLockPlanItem — запись lock-файла с оценкой: Problem — показывать красным, Reason — причина, Installable — можно ли выбрать для установки.
type WasmLockPlanItem struct {
	WasmLockEntry
	Status      string       `json:"status"`
	Problem     bool         `json:"problem,omitempty"`
	Reason      string       `json:"reason,omitempty"`
	Installable bool         `json:"installable"`
	InstalledAs string       `json:"installedVersion,omitempty"` // что стоит сейчас (для installed и differs)
	Deprecation *Deprecation `json:"deprecation,omitempty"`      // причина и замена (для deprecated)
}

// WasmLockExport — lock-файл установленных через магазин wasm-плагинов, по id.
func (m *Market) WasmLockExport() WasmLockFile {
	lock := m.WasmLock()
	f := WasmLockFile{Version: 1, Plugins: []WasmLockEntry{}}
	for id, r := range lock {
		f.Plugins = append(f.Plugins, WasmLockEntry{ID: id, Repo: r.Repo, Tag: r.Tag, Version: r.Version, Sha256: r.Sha256, Size: r.Size})
	}
	sort.Slice(f.Plugins, func(i, j int) bool { return f.Plugins[i].ID < f.Plugins[j].ID })
	return f
}

const maxLockPlugins = 100

// WasmLockPlan оценивает записи lock-файла (импортированного или своего): ничего не скачивает и не ставит. Список релизов каждого
// источника запрашивается один раз; sha256 сверяется по <id>.json релиза, а при установке — по самому файлу.
func (m *Market) WasmLockPlan(ctx context.Context, f WasmLockFile) []WasmLockPlanItem {
	entries := f.Plugins
	if len(entries) > maxLockPlugins {
		entries = entries[:maxLockPlugins]
	}
	host := m.wasmHost()
	have := map[string]WasmPlugin{}
	if host != nil {
		for _, p := range host.Plugins() {
			have[p.ID] = p
		}
	}
	lock := m.WasmLock()
	sources := map[string]string{} // нижний регистр → как записан
	for _, s := range m.Sources() {
		sources[strings.ToLower(s)] = s
	}
	type listing struct {
		rels []Release
		err  error
	}
	listed := map[string]listing{}
	var deps map[string][]Deprecation // источник → записи deprecated.json (грузим при первой надобности)

	out := make([]WasmLockPlanItem, 0, len(entries))
	for _, e := range entries {
		it := WasmLockPlanItem{WasmLockEntry: e}
		finish := func(status, reason string, problem, installable bool) {
			it.Status, it.Reason, it.Problem, it.Installable = status, reason, problem, installable
			out = append(out, it)
		}
		g := wasmTagRe.FindStringSubmatch(e.Tag)
		r, repoErr := ParseRepo(e.Repo)
		if g == nil || g[1] != e.ID || g[2] != e.Version || repoErr != nil || !sha256Re.MatchString(strings.ToLower(e.Sha256)) {
			finish(LockInvalid, "The lock entry is malformed (id, tag, version, repository or sha256).", true, false)
			continue
		}
		it.Sha256 = strings.ToLower(e.Sha256)
		key := strings.ToLower(r.String())
		src, ok := sources[key]
		if !ok {
			finish(LockNoSource, "The source "+r.String()+" is not added to the store. Add it first if you trust it.", true, false)
			continue
		}
		if m.builtin(e.ID) {
			finish(LockConflict, "A built-in plugin already uses this id.", true, false)
			continue
		}
		cur, loaded := have[e.ID]
		rec, ours := lock[e.ID]
		if (loaded || ours) && !(ours && strings.EqualFold(rec.Repo, src)) {
			finish(LockConflict, "A plugin with this id is already installed, but not from this source through the store.", true, false)
			continue
		}
		if loaded {
			it.InstalledAs = cur.Version
		}
		if deps == nil {
			deps = map[string][]Deprecation{}
			for _, sv := range m.Catalog(ctx, false) {
				if sv.Error == "" {
					deps[strings.ToLower(sv.Repo)] = sv.Deprecated
				}
			}
		}
		var dep *Deprecation
		for _, d := range deps[key] {
			if d.Kind == KindWasm && d.ID == e.ID {
				d := d
				dep = &d
			}
		}
		l, seen := listed[key]
		if !seen {
			l.rels, l.err = m.rel.List(ctx, r, m.token())
			listed[key] = l
		}
		if dep != nil {
			it.Deprecation = dep
			finish(LockDeprecated, "Deprecated: "+dep.Reason, true, false)
			continue
		}
		if l.err != nil {
			finish(LockUnavailable, "The source did not answer: "+l.err.Error(), true, false)
			continue
		}
		var rel *Release
		for i := range l.rels {
			if l.rels[i].Tag == e.Tag && !l.rels[i].Draft && !l.rels[i].Prerelease {
				rel = &l.rels[i]
				break
			}
		}
		if rel == nil {
			finish(LockRemoved, "Removed: release "+e.Tag+" no longer exists in "+src+".", true, false)
			continue
		}
		if in, ok := m.readWasmInfo(ctx, r, e.ID, e.Version, *rel); ok && in.Sha256 != it.Sha256 {
			finish(LockChanged, "The file of this release was changed after the lock was made — it will not be installed.", true, false)
			continue
		}
		switch {
		case loaded && cur.SHA256 == it.Sha256:
			finish(LockInstalled, "", false, false)
		case loaded:
			finish(LockDiffers, "Another version is installed ("+cur.Version+"): restoring brings back "+e.Version+".", false, true)
		default:
			finish(LockOK, "", false, true)
		}
	}
	return out
}
