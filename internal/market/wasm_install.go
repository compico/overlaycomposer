package market

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Установка автономных (wasm) плагинов из Releases источника. Один вызов WasmInstall ставит, обновляет и меняет версию (в том числе
// на более старую). Плагин — чужой код, поэтому: только по явной команде, только из источника в списке, файл сверяется с опубликованным
// sha256 (и с <id>.json, если он есть), проверяется как плагин (id и версия — те, что в теге) до того, как попасть в папку, и ничего
// не запускается при установке. Чужие плагины (поставленные руками или из другого источника) не перезаписываются.
// Что именно установлено (тег, sha256), записывается в состояние магазина — по нему набор можно воспроизвести на другом компьютере.

var (
	ErrNoWasmHost = errors.New("standalone plugins are unavailable")
	ErrNoRelease  = errors.New("release not found (reload the catalog)")
)

// WasmRecord — установленный через магазин wasm-плагин.
type WasmRecord struct {
	Repo    string `json:"repo"`
	Tag     string `json:"tag"`
	Version string `json:"version"`
	Sha256  string `json:"sha256"`
	Size    int64  `json:"size"`
	At      int64  `json:"at"`
}

// WasmPlugin — wasm-плагин, загруженный в OC (из любой папки, не только из магазина).
type WasmPlugin struct {
	ID          string
	Version     string
	SHA256      string
	File        string
	Permissions []string
}

// WasmHost — то, что магазину нужно от менеджера wasm-плагинов.
type WasmHost interface {
	Dir() string                                                         // папка, куда ставятся плагины
	Inspect(data []byte) (id, version string, perms []string, err error) // проверка файла как плагина, без запуска
	Plugins() []WasmPlugin                                               // загруженные плагины
	Rescan()                                                             // перечитать папку: новые файлы запускаются, изменённые перезапускаются
}

// SetWasmHost подключает менеджер wasm-плагинов (он создаётся позже магазина).
func (m *Market) SetWasmHost(h WasmHost) {
	m.mu.Lock()
	m.whost = h
	m.mu.Unlock()
}

func (m *Market) wasmHost() WasmHost {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.whost
}

// WasmLock — записи об установленных через магазин wasm-плагинах (для экспорта настроек), по id.
func (m *Market) WasmLock() map[string]WasmRecord {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]WasmRecord, len(m.st.Wasm))
	for k, v := range m.st.Wasm {
		out[k] = v
	}
	return out
}

// markWasmInstalled — копия записей каталога с отметками «установлен», «есть обновление», «чужой».
func (m *Market) markWasmInstalled(source string, list []WasmEntry) []WasmEntry {
	host := m.wasmHost()
	lock := m.WasmLock()
	have := map[string]WasmPlugin{}
	if host != nil {
		for _, p := range host.Plugins() {
			have[p.ID] = p
		}
	}
	out := make([]WasmEntry, len(list))
	for i, e := range list {
		cur, loaded := have[e.ID]
		rec, ours := lock[e.ID]
		if ours && !strings.EqualFold(rec.Repo, source) {
			ours = false // записан за другим источником: для этого он «чужой»
		}
		switch {
		case ours:
			e.Installed, e.InstalledVersion, e.InstalledTag = true, rec.Version, rec.Tag
			e.UpdateAvailable = newer(e.Version, rec.Version)
			if loaded && !e.NoInfo {
				e.AddedPermissions = addedPerms(cur.Permissions, e.Permissions)
			}
		case loaded || lock[e.ID].Repo != "":
			e.Blocked = "foreign"
		}
		out[i] = e
	}
	return out
}

func addedPerms(have, want []string) []string {
	set := map[string]bool{}
	for _, p := range have {
		set[p] = true
	}
	var add []string
	for _, p := range want {
		if !set[p] {
			add = append(add, p)
		}
	}
	sort.Strings(add)
	return add
}

// WasmInstalled — результат установки.
type WasmInstalled struct {
	ID               string   `json:"id"`
	Version          string   `json:"version"`
	Tag              string   `json:"tag"`
	Sha256           string   `json:"sha256"`
	Previous         string   `json:"previous,omitempty"`         // какая версия стояла (при обновлении, смене версии)
	AddedPermissions []string `json:"addedPermissions,omitempty"` // права новой версии, которых не было у прежней; они не выданы, пока пользователь не разрешит
}

// WasmInstall ставит плагин id из релиза tag (<id>-v<версия>) источника repoIn. sha — необязательный ожидаемый sha256 (из каталога
// или lock-файла): если файл релиза другой, установка отклоняется (ErrChanged).
func (m *Market) WasmInstall(ctx context.Context, repoIn, id, tag, sha string) (WasmInstalled, error) {
	var res WasmInstalled
	host := m.wasmHost()
	if host == nil || host.Dir() == "" {
		return res, ErrNoWasmHost
	}
	r, err := ParseRepo(repoIn)
	if err != nil {
		return res, err
	}
	g := wasmTagRe.FindStringSubmatch(tag)
	if g == nil || g[1] != id {
		return res, ErrNoRelease
	}
	version := g[2]
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
	rec, ours := m.st.Wasm[id]
	m.mu.Unlock()
	if !listed {
		return res, ErrNoSource
	}
	if m.builtin(id) {
		return res, ErrConflict
	}
	dst := filepath.Join(host.Dir(), id+".wasm")
	var cur *WasmPlugin
	for _, p := range host.Plugins() {
		if p.ID == id {
			p := p
			cur = &p
		}
	}
	_, statErr := os.Stat(dst)
	if (!ours && (cur != nil || statErr == nil)) || (ours && !strings.EqualFold(rec.Repo, key)) {
		return res, ErrConflict
	}
	if cur != nil && filepath.Clean(cur.File) != filepath.Clean(dst) {
		return res, ErrConflict // плагин с этим id загружен из другого файла: два файла с одним id не держим
	}

	releases, err := m.rel.List(ctx, r, m.token())
	if err != nil {
		return res, err
	}
	var rel *Release
	for i := range releases {
		if releases[i].Tag == tag && !releases[i].Draft && !releases[i].Prerelease {
			rel = &releases[i]
			break
		}
	}
	if rel == nil {
		return res, ErrNoRelease
	}
	data, got, err := m.wasmDownload(ctx, r, id, version, *rel)
	if err != nil {
		return res, err
	}
	if sha != "" && sha != got {
		return res, ErrChanged
	}
	gotID, gotVer, perms, err := host.Inspect(data)
	if err != nil {
		return res, fmt.Errorf("the file in the release is not a valid plugin: %w", err)
	}
	if gotID != id || gotVer != version {
		return res, fmt.Errorf("the file in release %s is plugin %q %s, not %q %s", tag, gotID, gotVer, id, version)
	}

	res = WasmInstalled{ID: id, Version: version, Tag: tag, Sha256: got}
	if cur != nil {
		res.Previous = cur.Version
		res.AddedPermissions = addedPerms(cur.Permissions, perms)
	}
	if cur == nil || cur.SHA256 != got { // тот же файл уже стоит — только обновляем запись
		if err := writeFileAtomic(dst, data); err != nil {
			return res, err
		}
	}
	m.mu.Lock()
	if m.st.Wasm == nil {
		m.st.Wasm = map[string]WasmRecord{}
	}
	m.st.Wasm[id] = WasmRecord{Repo: key, Tag: tag, Version: version, Sha256: got, Size: int64(len(data)), At: time.Now().Unix()}
	err = m.saveLocked()
	m.mu.Unlock()
	host.Rescan()
	m.dropWasmCache(r)
	return res, err
}

// wasmDownload скачивает <id>.wasm релиза и проверяет его: размер, опубликованный sha256 (<id>.wasm.sha256) и <id>.json, если он есть.
func (m *Market) wasmDownload(ctx context.Context, r Repo, id, version string, rel Release) ([]byte, string, error) {
	var wasmAsset, shaAsset *ReleaseAsset
	for i := range rel.Assets {
		switch rel.Assets[i].Name {
		case id + ".wasm":
			wasmAsset = &rel.Assets[i]
		case id + ".wasm.sha256":
			shaAsset = &rel.Assets[i]
		}
	}
	if wasmAsset == nil || shaAsset == nil {
		return nil, "", fmt.Errorf("release %s has no %s.wasm or %s.wasm.sha256", rel.Tag, id, id)
	}
	if wasmAsset.Size <= 0 || wasmAsset.Size > maxWasmSize {
		return nil, "", fmt.Errorf("release %s: file size %d is not acceptable", rel.Tag, wasmAsset.Size)
	}
	raw, err := m.rel.Asset(ctx, r, m.token(), shaAsset.ID, 1024)
	if err != nil {
		return nil, "", err
	}
	published := strings.ToLower(strings.TrimSpace(strings.SplitN(strings.TrimSpace(string(raw)), " ", 2)[0]))
	if !sha256Re.MatchString(published) {
		return nil, "", fmt.Errorf("release %s: unreadable %s.wasm.sha256", rel.Tag, id)
	}
	data, err := m.rel.Asset(ctx, r, m.token(), wasmAsset.ID, maxWasmSize)
	if err != nil {
		return nil, "", err
	}
	got := sha256Hex(data)
	if int64(len(data)) != wasmAsset.Size || got != published {
		return nil, "", fmt.Errorf("release %s: the downloaded file does not match its published checksum", rel.Tag)
	}
	if in, ok := m.readWasmInfo(ctx, r, id, version, rel); ok && in.Sha256 != got {
		return nil, "", fmt.Errorf("release %s: %s.json disagrees with the file", rel.Tag, id)
	}
	return data, got, nil
}

func (m *Market) dropWasmCache(r Repo) {
	m.wasm.mu.Lock()
	delete(m.wasm.m, strings.ToLower(r.Owner+"/"+r.Name))
	m.wasm.mu.Unlock()
}

// WasmUninstall удаляет плагин, поставленный через магазин. Права и настройки плагина остаются (вернётся — заработает как прежде).
func (m *Market) WasmUninstall(id string) error {
	host := m.wasmHost()
	if host == nil || host.Dir() == "" {
		return ErrNoWasmHost
	}
	m.opMu.Lock()
	defer m.opMu.Unlock()
	m.mu.Lock()
	_, ours := m.st.Wasm[id]
	m.mu.Unlock()
	if !ours {
		return ErrNotOurs
	}
	dst := filepath.Join(host.Dir(), id+".wasm")
	if err := os.Remove(dst); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	m.mu.Lock()
	delete(m.st.Wasm, id)
	err := m.saveLocked()
	m.mu.Unlock()
	host.Rescan()
	return err
}

// writeFileAtomic пишет файл во временный (имя с точкой — менеджер такие не читает) и переименовывает поверх назначения.
func writeFileAtomic(dst string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp := filepath.Join(filepath.Dir(dst), "."+filepath.Base(dst)+".tmp")
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
