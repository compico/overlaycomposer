package market

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

var errUnavailable = errors.New("offline")

func removeFile(p string) error { return os.Remove(p) }

func planByID(items []WasmLockPlanItem) map[string]WasmLockPlanItem {
	out := map[string]WasmLockPlanItem{}
	for _, it := range items {
		out[it.ID] = it
	}
	return out
}

func TestWasmLockExportRoundTripAndPlan(t *testing.T) {
	m, h, f := installEnv(t)
	ctx := context.Background()
	f.addRelease("music", "0.9.0", "music 0.9.0 storage", true)
	f.addRelease("music", "1.0.0", "music 1.0.0 storage", true)
	if _, err := m.WasmInstall(ctx, "o/n", "music", "music-v0.9.0", ""); err != nil {
		t.Fatal(err)
	}
	lock := m.WasmLockExport()
	if lock.Version != 1 || len(lock.Plugins) != 1 || lock.Plugins[0].Tag != "music-v0.9.0" || lock.Plugins[0].Sha256 != shaOf("music 0.9.0 storage") {
		t.Fatalf("%+v", lock)
	}
	// то же состояние: ничего делать не нужно
	if it := planByID(m.WasmLockPlan(ctx, lock))["music"]; it.Status != LockInstalled || it.Problem || it.Installable {
		t.Fatalf("%+v", it)
	}
	// обновили — по lock можно вернуть старую версию
	if _, err := m.WasmInstall(ctx, "o/n", "music", "music-v1.0.0", ""); err != nil {
		t.Fatal(err)
	}
	it := planByID(m.WasmLockPlan(ctx, lock))["music"]
	if it.Status != LockDiffers || !it.Installable || it.Problem || it.InstalledAs != "1.0.0" {
		t.Fatalf("%+v", it)
	}
	// и вернуть её: ровно записанный файл
	if _, err := m.WasmInstall(ctx, "o/n", "music", it.Tag, it.Sha256); err != nil {
		t.Fatal(err)
	}
	if got := m.WasmLock()["music"]; got.Version != "0.9.0" {
		t.Fatalf("%+v", got)
	}
	_ = h
}

func TestWasmLockPlanProblems(t *testing.T) {
	m, _, f := installEnv(t)
	ctx := context.Background()
	f.addRelease("fresh", "1.0.0", "fresh 1.0.0", true)
	f.addRelease("changed", "1.0.0", "changed 1.0.0", true)
	entry := func(id, repo, ver, sha string) WasmLockEntry {
		return WasmLockEntry{ID: id, Repo: repo, Tag: id + "-v" + ver, Version: ver, Sha256: sha}
	}
	plan := planByID(m.WasmLockPlan(ctx, WasmLockFile{Version: 1, Plugins: []WasmLockEntry{
		entry("fresh", "o/n", "1.0.0", shaOf("fresh 1.0.0")),    // ok
		entry("gone", "o/n", "1.0.0", goodSha),                  // релиза нет
		entry("changed", "o/n", "1.0.0", goodSha),               // sha другой
		entry("other", "stranger/repo", "1.0.0", goodSha),       // источник не добавлен
		{ID: "bad", Repo: "o/n", Tag: "nope", Version: "1.0.0"}, // мусор
	}}))
	want := map[string]struct {
		status  string
		problem bool
		inst    bool
		reason  string
	}{
		"fresh":   {LockOK, false, true, ""},
		"gone":    {LockRemoved, true, false, "Removed"},
		"changed": {LockChanged, true, false, "changed"},
		"other":   {LockNoSource, true, false, "not added"},
		"bad":     {LockInvalid, true, false, "malformed"},
	}
	for id, w := range want {
		it := plan[id]
		if it.Status != w.status || it.Problem != w.problem || it.Installable != w.inst || !strings.Contains(it.Reason, w.reason) {
			t.Errorf("%s: %+v", id, it)
		}
	}
}

func TestWasmLockPlanUnavailableAndConflict(t *testing.T) {
	m, h, f := installEnv(t)
	ctx := context.Background()
	f.addRelease("music", "1.0.0", "music 1.0.0", true)
	// плагин с этим id поставлен руками
	if err := writeFileAtomic(h.dir+"/music.wasm", []byte("music 0.1.0")); err != nil {
		t.Fatal(err)
	}
	e := WasmLockEntry{ID: "music", Repo: "o/n", Tag: "music-v1.0.0", Version: "1.0.0", Sha256: shaOf("music 1.0.0")}
	if it := planByID(m.WasmLockPlan(ctx, WasmLockFile{Plugins: []WasmLockEntry{e}}))["music"]; it.Status != LockConflict || it.Installable {
		t.Fatalf("%+v", it)
	}
	if err := removeFile(h.dir + "/music.wasm"); err != nil {
		t.Fatal(err)
	}
	f.err = errUnavailable
	if it := planByID(m.WasmLockPlan(ctx, WasmLockFile{Plugins: []WasmLockEntry{e}}))["music"]; it.Status != LockUnavailable || !it.Problem {
		t.Fatalf("%+v", it)
	}
}

func TestWasmLockPlanDeprecated(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	h := &fakeHost{dir: t.TempDir()}
	e.m.SetWasmHost(h)
	dep := `[{"id":"osu-skills","kind":"wasm","since":"2026-11-17","reason":"Merged into osu-tools.","replacedBy":{"id":"osu-tools","kind":"wasm","repo":"bob/tools"}}]`
	e.repos["bob/tools"] = makeZip(t, append(plug("alpha", "1.0.0"), file{name: "repo-abc/deprecated.json", body: dep})...)
	if _, err := e.m.AddSource(ctx, "bob/tools"); err != nil {
		t.Fatal(err)
	}
	e.rel.addRelease("osu-skills", "0.6.2", "osu-skills 0.6.2", true)
	lock := WasmLockFile{Plugins: []WasmLockEntry{{ID: "osu-skills", Repo: "bob/tools", Tag: "osu-skills-v0.6.2", Version: "0.6.2", Sha256: shaOf("osu-skills 0.6.2")}}}
	it := planByID(e.m.WasmLockPlan(ctx, lock))["osu-skills"]
	if it.Status != LockDeprecated || !it.Problem || it.Installable || it.Deprecation == nil || it.Deprecation.ReplacedBy == nil || it.Deprecation.ReplacedBy.ID != "osu-tools" || !strings.Contains(it.Reason, "Merged") {
		t.Fatalf("%+v", it)
	}
}
