package market

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeHost — менеджер плагинов: «читает» файлы папки, а проверка файла — по его тексту "id version perms".
type fakeHost struct {
	dir     string
	rescans int
}

func (h *fakeHost) Dir() string { return h.dir }
func (h *fakeHost) Inspect(data []byte) (string, string, []string, error) {
	f := strings.Fields(string(data))
	if len(f) < 2 {
		return "", "", nil, errors.New("not a plugin")
	}
	return f[0], f[1], f[2:], nil
}
func (h *fakeHost) Plugins() []WasmPlugin {
	var out []WasmPlugin
	ents, _ := os.ReadDir(h.dir)
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".wasm") || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		p := filepath.Join(h.dir, e.Name())
		b, _ := os.ReadFile(p)
		f := strings.Fields(string(b))
		sum := sha256.Sum256(b)
		out = append(out, WasmPlugin{ID: f[0], Version: f[1], SHA256: hex.EncodeToString(sum[:]), File: p, Permissions: f[2:]})
	}
	return out
}
func (h *fakeHost) Rescan() { h.rescans++ }

func shaOf(s string) string { b := sha256.Sum256([]byte(s)); return hex.EncodeToString(b[:]) }

// release собирает релиз <id>-v<ver> с файлами: .wasm (тело body), .wasm.sha256 и, если withJSON, <id>.json.
func (f *fakeReleases) addRelease(id, ver, body string, withJSON bool) {
	base := int64(len(f.list)+1) * 10
	rel := Release{Tag: id + "-v" + ver, Assets: []ReleaseAsset{
		{ID: base + 1, Name: id + ".wasm", Size: int64(len(body))},
		{ID: base + 2, Name: id + ".wasm.sha256", Size: 80},
	}}
	if f.files == nil {
		f.files = map[int64]string{}
	}
	f.files[base+1] = body
	f.files[base+2] = shaOf(body) + "  " + id + ".wasm\n"
	if withJSON {
		rel.Assets = append(rel.Assets, ReleaseAsset{ID: base + 3, Name: id + ".json", Size: 300})
		perms := "[]"
		if w := strings.Fields(body); len(w) > 2 {
			perms = `["` + strings.Join(w[2:], `","`) + `"]`
		}
		f.files[base+3] = fmt.Sprintf(`{"id":%q,"version":%q,"sha256":%q,"size":%d,"meta":{"id":%q,"name":"M","version":%q,"permissions":%s}}`, id, ver, shaOf(body), len(body), id, ver, perms)
	}
	f.list = append([]Release{rel}, f.list...)
}

func installEnv(t *testing.T) (*Market, *fakeHost, *fakeReleases) {
	t.Helper()
	f := &fakeReleases{}
	m := newWasmMarket(t, f)
	m.st.Installed = map[string]Record{}
	h := &fakeHost{dir: t.TempDir()}
	m.SetWasmHost(h)
	return m, h, f
}

func TestWasmInstallUpdateDowngradeAndLock(t *testing.T) {
	m, h, f := installEnv(t)
	f.addRelease("music", "0.9.0", "music 0.9.0 storage", true)
	f.addRelease("music", "1.0.0", "music 1.0.0 storage chat.send", true)
	f.addRelease("music", "0.8.1", "music 0.8.1 storage", false) // старый релиз без json
	ctx := context.Background()

	res, err := m.WasmInstall(ctx, "o/n", "music", "music-v0.9.0", "")
	if err != nil || res.Version != "0.9.0" || res.Previous != "" || res.Sha256 != shaOf("music 0.9.0 storage") {
		t.Fatalf("%+v %v", res, err)
	}
	if b, _ := os.ReadFile(filepath.Join(h.dir, "music.wasm")); string(b) != "music 0.9.0 storage" || h.rescans != 1 {
		t.Fatalf("файл %q, rescans %d", b, h.rescans)
	}
	if ents, _ := os.ReadDir(h.dir); len(ents) != 1 {
		t.Fatalf("временные файлы остались: %v", ents)
	}
	rec := m.WasmLock()["music"]
	if rec.Tag != "music-v0.9.0" || rec.Version != "0.9.0" || rec.Sha256 != shaOf("music 0.9.0 storage") || rec.Repo != "o/n" || rec.At == 0 {
		t.Fatalf("lock: %+v", rec)
	}

	// обновление: показывает прежнюю версию и новые права (они не выдаются автоматически)
	res, err = m.WasmInstall(ctx, "o/n", "music", "music-v1.0.0", "")
	if err != nil || res.Previous != "0.9.0" || strings.Join(res.AddedPermissions, ",") != "chat.send" {
		t.Fatalf("%+v %v", res, err)
	}
	// смена на старую версию — явным вызовом, у релиза без json тоже
	res, err = m.WasmInstall(ctx, "o/n", "music", "music-v0.8.1", shaOf("music 0.8.1 storage"))
	if err != nil || res.Version != "0.8.1" || res.Previous != "1.0.0" {
		t.Fatalf("%+v %v", res, err)
	}
	if got := m.WasmLock()["music"]; got.Version != "0.8.1" || got.Tag != "music-v0.8.1" {
		t.Fatalf("lock после понижения: %+v", got)
	}
	// повторная установка того же файла: файл не переписывается, запись остаётся
	before := h.rescans
	if _, err := m.WasmInstall(ctx, "o/n", "music", "music-v0.8.1", ""); err != nil {
		t.Fatal(err)
	}
	if h.rescans != before+1 {
		t.Fatalf("rescans %d", h.rescans)
	}

	// запись переживает перезапуск
	m.mu.Lock()
	m.file = filepath.Join(t.TempDir(), "market.json")
	_ = m.saveLocked()
	m.mu.Unlock()
	m2, err := New(Config{File: m.file, Dir: t.TempDir(), Releases: f})
	if err != nil || m2.WasmLock()["music"].Tag != "music-v0.8.1" {
		t.Fatalf("после перезапуска: %v %+v", err, m2.WasmLock())
	}

	if err := m.WasmUninstall("music"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(h.dir, "music.wasm")); !os.IsNotExist(err) || len(m.WasmLock()) != 0 {
		t.Fatal("не удалён")
	}
	if err := m.WasmUninstall("music"); !errors.Is(err, ErrNotOurs) {
		t.Fatalf("повторное удаление: %v", err)
	}
}

func TestWasmInstallRejectsBadFiles(t *testing.T) {
	m, h, f := installEnv(t)
	ctx := context.Background()
	install := func(tag, sha string) error {
		_, err := m.WasmInstall(ctx, "o/n", strings.SplitN(tag, "-v", 2)[0], tag, sha)
		return err
	}
	f.addRelease("good", "1.0.0", "good 1.0.0", true)
	if err := install("good-v1.0.0", strings.Repeat("0", 64)); !errors.Is(err, ErrChanged) {
		t.Fatalf("другой sha256: %v", err)
	}
	f.addRelease("lying", "1.0.0", "lying 1.0.0", false)
	// тело подменено, а опубликованный sha256 прежний
	f.files[f.list[0].Assets[0].ID] = "lying 1.0.0 EVIL"
	f.list[0].Assets[0].Size = int64(len("lying 1.0.0 EVIL"))
	if err := install("lying-v1.0.0", ""); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("файл не по sha256: %v", err)
	}
	f.addRelease("other", "1.0.0", "different 1.0.0", false) // внутри другой плагин
	if err := install("other-v1.0.0", ""); err == nil || !strings.Contains(err.Error(), "not") {
		t.Fatalf("чужой id внутри: %v", err)
	}
	f.addRelease("ver", "1.0.0", "ver 2.0.0", false) // версия внутри не та
	if err := install("ver-v1.0.0", ""); err == nil {
		t.Fatal("версия внутри не совпала с тегом")
	}
	f.addRelease("junk", "1.0.0", "x", false)
	if err := install("junk-v1.0.0", ""); err == nil || !strings.Contains(err.Error(), "valid plugin") {
		t.Fatalf("не плагин: %v", err)
	}
	if err := install("nope-v1.0.0", ""); !errors.Is(err, ErrNoRelease) {
		t.Fatalf("нет релиза: %v", err)
	}
	if _, err := m.WasmInstall(ctx, "o/n", "good", "other-v1.0.0", ""); !errors.Is(err, ErrNoRelease) {
		t.Fatalf("тег чужого плагина: %v", err)
	}
	f.list = append(f.list, Release{Tag: "pre-v1.0.0", Prerelease: true})
	if err := install("pre-v1.0.0", ""); !errors.Is(err, ErrNoRelease) {
		t.Fatalf("предрелиз: %v", err)
	}
	if _, err := m.WasmInstall(ctx, "x/y", "good", "good-v1.0.0", ""); !errors.Is(err, ErrNoSource) {
		t.Fatalf("источник не в списке: %v", err)
	}
	if ents, _ := os.ReadDir(h.dir); len(ents) != 0 || len(m.WasmLock()) != 0 || h.rescans != 0 {
		t.Fatalf("ничего не должно было установиться: %v %v", ents, m.WasmLock())
	}
}

func TestWasmInstallDoesNotTouchForeignPlugins(t *testing.T) {
	m, h, f := installEnv(t)
	f.addRelease("music", "1.0.0", "music 1.0.0", true)
	// поставлен руками (под другим именем файла и под тем же)
	manual := filepath.Join(h.dir, "music-manual.wasm")
	_ = os.WriteFile(manual, []byte("music 0.5.0"), 0o644)
	if _, err := m.WasmInstall(context.Background(), "o/n", "music", "music-v1.0.0", ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("чужой плагин: %v", err)
	}
	_ = os.Remove(manual)
	_ = os.WriteFile(filepath.Join(h.dir, "music.wasm"), []byte("music 0.5.0"), 0o644)
	if _, err := m.WasmInstall(context.Background(), "o/n", "music", "music-v1.0.0", ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("тот же файл, но не из магазина: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(h.dir, "music.wasm")); string(b) != "music 0.5.0" {
		t.Fatal("чужой файл перезаписан")
	}
	cat := m.WasmCatalog(context.Background(), true)[0].Plugins[0]
	if cat.Blocked != "foreign" || cat.Installed {
		t.Fatalf("каталог: %+v", cat)
	}
	// плагин, записанный за другим источником, тоже чужой
	m.st.Wasm = map[string]WasmRecord{"music": {Repo: "someone/else", Tag: "music-v0.5.0", Version: "0.5.0"}}
	if _, err := m.WasmInstall(context.Background(), "o/n", "music", "music-v1.0.0", ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("другой источник: %v", err)
	}
	if err := m.WasmUninstall("nothing"); !errors.Is(err, ErrNotOurs) {
		t.Fatalf("удаление чужого: %v", err)
	}
}

func TestWasmCatalogMarksInstalledAndUpdates(t *testing.T) {
	m, _, f := installEnv(t)
	f.addRelease("music", "0.9.0", "music 0.9.0 storage", true)
	f.addRelease("music", "1.0.0", "music 1.0.0 storage chat.send", true)
	if _, err := m.WasmInstall(context.Background(), "o/n", "music", "music-v0.9.0", ""); err != nil {
		t.Fatal(err)
	}
	e := m.WasmCatalog(context.Background(), true)[0].Plugins[0]
	if !e.Installed || e.InstalledVersion != "0.9.0" || e.InstalledTag != "music-v0.9.0" || !e.UpdateAvailable || e.Version != "1.0.0" || e.Blocked != "" {
		t.Fatalf("%+v", e)
	}
	if len(e.AddedPermissions) != 1 || e.AddedPermissions[0] != "chat.send" {
		t.Fatalf("новые права: %v", e.AddedPermissions)
	}
	if _, err := m.WasmInstall(context.Background(), "o/n", "music", "music-v1.0.0", ""); err != nil {
		t.Fatal(err)
	}
	if e = m.WasmCatalog(context.Background(), false)[0].Plugins[0]; e.UpdateAvailable || e.InstalledVersion != "1.0.0" {
		t.Fatalf("после обновления: %+v", e)
	}
}

func TestWasmInstallWithoutHost(t *testing.T) {
	m := newWasmMarket(t, &fakeReleases{})
	if _, err := m.WasmInstall(context.Background(), "o/n", "a", "a-v1.0.0", ""); !errors.Is(err, ErrNoWasmHost) {
		t.Fatal(err)
	}
	if err := m.WasmUninstall("a"); !errors.Is(err, ErrNoWasmHost) {
		t.Fatal(err)
	}
}

// addChangelog прикладывает к уже добавленному релизу <id>-v<ver> ассет <id>.changelog.json.
func (f *fakeReleases) addChangelog(id, ver, body string) {
	for i := range f.list {
		if f.list[i].Tag != id+"-v"+ver {
			continue
		}
		cid := int64(9000 + len(f.files))
		f.list[i].Assets = append(f.list[i].Assets, ReleaseAsset{ID: cid, Name: id + ".changelog.json", Size: int64(len(body))})
		f.files[cid] = body
		return
	}
	panic("нет релиза " + id + "-v" + ver)
}

func TestWasmCatalogChangelogOnlyForInstalled(t *testing.T) {
	m, _, f := installEnv(t)
	f.addRelease("music", "0.9.0", "music 0.9.0 storage", true)
	f.addRelease("music", "1.0.0", "music 1.0.0 storage", true)
	f.addRelease("other", "1.0.0", "other 1.0.0", true)
	f.addChangelog("music", "1.0.0", `[{"version":"0.9.0","changes":["a"]},{"version":"1.0.0","changes":["b"],"migration":"сделайте x"}]`)
	f.addChangelog("other", "1.0.0", `[{"version":"1.0.0","changes":["c"]}]`)
	cat := func() map[string]WasmEntry {
		out := map[string]WasmEntry{}
		for _, e := range m.WasmCatalog(context.Background(), true)[0].Plugins {
			out[e.ID] = e
		}
		return out
	}
	if e := cat()["music"]; e.Changelog != nil {
		t.Fatalf("у неустановленного журнал не нужен: %+v", e.Changelog)
	}
	if _, err := m.WasmInstall(context.Background(), "o/n", "music", "music-v0.9.0", ""); err != nil {
		t.Fatal(err)
	}
	e := cat()["music"]
	if len(e.Changelog) != 2 || e.Changelog[1].Version != "1.0.0" || e.Changelog[1].Migration != "сделайте x" {
		t.Fatalf("журнал установленного: %+v", e.Changelog)
	}
	if o := cat()["other"]; o.Changelog != nil {
		t.Fatalf("чужой журнал: %+v", o.Changelog)
	}
}

func TestWasmChangelogIgnoredWhenBrokenOrHuge(t *testing.T) {
	m, _, f := installEnv(t)
	f.addRelease("a", "1.0.0", "a 1.0.0", true)
	f.addRelease("b", "1.0.0", "b 1.0.0", true)
	f.addChangelog("a", "1.0.0", `не json`)
	f.addChangelog("b", "1.0.0", `[{"version":"1.0.0","changes":["`+strings.Repeat("x", maxChangelogSize)+`"]}]`)
	for _, id := range []string{"a", "b"} {
		if _, err := m.WasmInstall(context.Background(), "o/n", id, id+"-v1.0.0", ""); err != nil {
			t.Fatalf("%s: журнал не должен мешать установке: %v", id, err)
		}
	}
	for _, e := range m.WasmCatalog(context.Background(), true)[0].Plugins {
		if e.Changelog != nil || e.NoInfo {
			t.Fatalf("%s: %+v", e.ID, e)
		}
	}
}

func TestUpdatesIncludeWasmPlugins(t *testing.T) {
	m, _, f := installEnv(t)
	ctx := context.Background()
	f.addRelease("music", "0.8.0", "music 0.8.0 storage", true)
	f.addRelease("music", "0.9.0", "music 0.9.0 storage", true)
	f.addRelease("music", "1.0.0", "music 1.0.0 storage chat.send", true)
	f.addChangelog("music", "1.0.0", `[{"version":"0.8.0","changes":["old"]},{"version":"0.9.0","migration":"m9"},{"version":"1.0.0","changes":["new"]}]`)
	if _, err := m.WasmInstall(ctx, "o/n", "music", "music-v0.8.0", ""); err != nil {
		t.Fatal(err)
	}
	ups, ok := m.Updates(ctx, true)
	if !ok || len(ups) != 1 {
		t.Fatalf("%+v %v", ups, ok)
	}
	u := ups[0]
	if u.Kind != "wasm" || u.ID != "music" || u.From != "0.8.0" || u.To != "1.0.0" || len(u.AddedPermissions) != 1 || u.AddedPermissions[0] != "chat.send" {
		t.Fatalf("%+v", u)
	}
	if len(u.Changelog) != 2 || u.Changelog[0].Version != "0.9.0" || u.Changelog[1].Version != "1.0.0" {
		t.Fatalf("пропущенные версии: %+v", u.Changelog)
	}
	// после обновления до последней версии уведомлять не о чем
	if _, err := m.WasmInstall(ctx, "o/n", "music", "music-v1.0.0", ""); err != nil {
		t.Fatal(err)
	}
	if ups, ok = m.Updates(ctx, true); !ok || len(ups) != 0 {
		t.Fatalf("%+v %v", ups, ok)
	}
}

func TestUpdatesWasmCatalogFailure(t *testing.T) {
	m, _, f := installEnv(t)
	ctx := context.Background()
	f.addRelease("music", "0.8.0", "music 0.8.0", true)
	// без поставленных через магазин плагинов сбой каталога релизов проверку не ломает
	f.err = errors.New("rate limited")
	if _, ok := m.Updates(ctx, true); ok { // у installEnv нет виджетного каталога: ok=false и без этого, проверяем только отсутствие паники
		t.Log("ok без источников виджетов")
	}
	f.err = nil
	if _, err := m.WasmInstall(ctx, "o/n", "music", "music-v0.8.0", ""); err != nil {
		t.Fatal(err)
	}
	f.err = errors.New("rate limited")
	if _, ok := m.Updates(ctx, true); ok {
		t.Fatal("поставленный wasm-плагин и сбой каталога релизов: ответ ненадёжен")
	}
}

func TestWasmInstallMinHost(t *testing.T) {
	m, h, f := installEnv(t)
	m.hostVer = "v2026.11.23.6"
	f.addRelease("chat", "0.1.0", "chat 0.1.0 storage", true)
	f.addRelease("chat", "0.2.0", "chat 0.2.0 storage", true)
	// 0.2.0 требует OC новее установленного; 0.1.0 — без требований
	for i := range f.list {
		if f.list[i].Tag != "chat-v0.2.0" {
			continue
		}
		for _, a := range f.list[i].Assets {
			if a.Name == "chat.json" {
				f.files[a.ID] = strings.Replace(f.files[a.ID], `"permissions"`, `"minHost":"v2026.11.25.7","permissions"`, 1)
			}
		}
	}
	ctx := context.Background()

	srcs := m.WasmCatalog(ctx, true)
	e := srcs[0].Plugins[0]
	if e.Version != "0.2.0" || e.MinHost != "v2026.11.25.7" || !e.HostTooOld {
		t.Fatalf("каталог: %+v", e)
	}

	var tooOld *HostTooOldError
	if _, err := m.WasmInstall(ctx, "o/n", "chat", "chat-v0.2.0", ""); !errors.As(err, &tooOld) || tooOld.Need != "v2026.11.25.7" {
		t.Fatalf("установка: %v", err)
	}
	if _, err := os.Stat(filepath.Join(h.dir, "chat.wasm")); err == nil {
		t.Fatal("при отказе файл не кладётся")
	}
	// старая версия без требований ставится
	if _, err := m.WasmInstall(ctx, "o/n", "chat", "chat-v0.1.0", ""); err != nil {
		t.Fatal(err)
	}
	// после обновления приложения (и для dev-сборки) та же версия доступна
	for _, v := range []string{"v2026.11.25.7", "dev"} {
		m.hostVer = v
		if _, err := m.WasmInstall(ctx, "o/n", "chat", "chat-v0.2.0", ""); err != nil {
			t.Fatalf("хост %s: %v", v, err)
		}
		if _, err := m.WasmInstall(ctx, "o/n", "chat", "chat-v0.1.0", ""); err != nil {
			t.Fatal(err)
		}
	}
}
