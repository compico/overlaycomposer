package content

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/compico/overlaycomposer/internal/bundle"
	"github.com/compico/overlaycomposer/internal/market"
)

type fakeHost struct {
	lock    market.WasmLockFile
	plan    []market.WasmLockPlanItem
	loaded  map[string]LoadedPlugin
	grants  map[string][]string
	configs map[string]json.RawMessage
	failFor string
	// install имитирует установку: плагин становится загруженным с sha из lock
	installed []string
	fileSha   map[string]string // если задан — загруженный файл не совпадает с lock
}

func (h *fakeHost) Lock() market.WasmLockFile { return h.lock }
func (h *fakeHost) Plan(_ context.Context, f market.WasmLockFile) []market.WasmLockPlanItem {
	return h.plan
}
func (h *fakeHost) Install(_ context.Context, repo, id, tag, sha string) error {
	if id == h.failFor {
		return errors.New("changed")
	}
	h.installed = append(h.installed, id+"@"+sha)
	lp := h.loaded[id]
	lp.SHA256 = sha
	if s, ok := h.fileSha[id]; ok {
		lp.SHA256 = s
	}
	h.loaded[id] = lp
	return nil
}
func (h *fakeHost) Loaded(id string) (LoadedPlugin, bool) { lp, ok := h.loaded[id]; return lp, ok }
func (h *fakeHost) LoadedIDs() []string {
	var ids []string
	for id := range h.loaded {
		ids = append(ids, id)
	}
	return ids
}
func (h *fakeHost) SetGrants(id string, p []string) error { h.grants[id] = p; return nil }
func (h *fakeHost) SetConfig(id string, c json.RawMessage) error {
	if _, ok := h.loaded[id]; !ok {
		return errors.New("no such plugin")
	}
	h.configs[id] = c
	lp := h.loaded[id]
	lp.Config = c
	h.loaded[id] = lp
	return nil
}

const shaA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func entry(id string) market.WasmLockEntry {
	return market.WasmLockEntry{ID: id, Repo: "o/n", Tag: id + "-v1.0.0", Version: "1.0.0", Sha256: shaA}
}

func newHost() *fakeHost {
	return &fakeHost{loaded: map[string]LoadedPlugin{}, grants: map[string][]string{}, configs: map[string]json.RawMessage{}}
}

func TestPluginsExportGrantsOnlyForLockedPlugins(t *testing.T) {
	h := newHost()
	h.lock = market.WasmLockFile{Version: 1, Plugins: []market.WasmLockEntry{entry("music")}}
	h.loaded["music"] = LoadedPlugin{Granted: []string{"net", "chat.send"}, Config: json.RawMessage(`{"token":"secret"}`)}
	h.loaded["manual"] = LoadedPlugin{Granted: []string{"net"}}
	raw, err := (&Plugins{Host: h}).Export(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if !strings.Contains(s, `"chat.send","net"`) || strings.Contains(s, "manual") || strings.Contains(s, "secret") {
		t.Fatalf("%s", s)
	}
}

func TestPluginsPlanMapsStatuses(t *testing.T) {
	h := newHost()
	h.plan = []market.WasmLockPlanItem{
		{WasmLockEntry: entry("ok"), Status: market.LockOK, Installable: true},
		{WasmLockEntry: entry("same"), Status: market.LockInstalled},
		{WasmLockEntry: entry("diff"), Status: market.LockDiffers, Installable: true, Reason: "Another version is installed."},
		{WasmLockEntry: entry("old"), Status: market.LockDeprecated, Problem: true, Reason: "Deprecated: merged.",
			Deprecation: &market.Deprecation{ReplacedBy: &market.Replacement{ID: "new", Repo: "o/n"}}},
		{WasmLockEntry: entry("gone"), Status: market.LockRemoved, Problem: true, Reason: "Removed: no release."},
	}
	raw := mustRaw(t, pluginsData{Lock: market.WasmLockFile{Version: 1}, Grants: map[string][]string{"ok": {"net"}}})
	items, err := (&Plugins{Host: h}).Plan(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	got := statuses(items)
	if got["ok"].Status != bundle.StatusNew || got["same"].Status != bundle.StatusSame || got["diff"].Status != bundle.StatusReplace ||
		got["old"].Status != bundle.StatusProblem || got["gone"].Status != bundle.StatusProblem {
		t.Fatalf("%+v", got)
	}
	if !strings.Contains(got["ok"].Reason, "Saved permissions (net)") || !strings.Contains(got["old"].Reason, "Replacement: new (o/n)") {
		t.Fatalf("%+v %+v", got["ok"], got["old"])
	}
}

func TestPluginsApplyGrantsOnlyWhenShaMatches(t *testing.T) {
	h := newHost()
	h.loaded["music"] = LoadedPlugin{Requested: []string{"net", "chat.send", "storage"}}
	h.loaded["odd"] = LoadedPlugin{Requested: []string{"net"}}
	h.fileSha = map[string]string{"odd": strings.Repeat("b", 64)} // файл не совпал с lock
	raw := mustRaw(t, pluginsData{
		Lock:   market.WasmLockFile{Version: 1, Plugins: []market.WasmLockEntry{entry("music"), entry("odd"), entry("bad")}},
		Grants: map[string][]string{"music": {"net", "chat.send", "removed.perm"}, "odd": {"net"}},
	})
	h.failFor = "bad"
	out, err := (&Plugins{Host: h}).Apply(context.Background(), raw, map[string]string{"music": bundle.ModeReplace, "odd": bundle.ModeReplace, "bad": bundle.ModeReplace})
	if err != nil || len(out) != 3 {
		t.Fatalf("%+v %v", out, err)
	}
	if len(h.installed) != 2 || h.installed[0] != "music@"+shaA {
		t.Fatalf("%v", h.installed)
	}
	if g := h.grants["music"]; len(g) != 2 || g[0] != "net" || g[1] != "chat.send" {
		t.Fatalf("права music: %v", g)
	}
	if !strings.Contains(out[0].Note, "Not granted yet (confirm in Plugins): storage") || strings.Contains(out[0].Note, "removed.perm") {
		t.Fatalf("%+v", out[0])
	}
	if _, ok := h.grants["odd"]; ok || !strings.Contains(out[1].Note, "do not match") && !strings.Contains(out[1].Note, "does not match") {
		t.Fatalf("права odd при другом sha: %v %+v", h.grants, out[1])
	}
	if out[2].OK || out[2].Error != "changed" {
		t.Fatalf("%+v", out[2])
	}
}

func TestPluginSettingsIsOptInAndNeedsInstalledPlugin(t *testing.T) {
	h := newHost()
	h.loaded["music"] = LoadedPlugin{Config: json.RawMessage(`{"reward":"x","token":"t"}`)}
	h.loaded["empty"] = LoadedPlugin{Config: json.RawMessage(`{}`)}
	sec := &PluginSettingsSection{Host: h}
	var _ bundle.OptIn = sec

	// по умолчанию в файл не попадает, но в явном экспорте и в ExportAll есть
	svc := bundle.New("t", &Plugins{Host: h}, sec)
	f, err := svc.Export(context.Background(), nil)
	if err != nil || f.Sections["plugin-settings"] != nil || f.Sections["plugins"] == nil {
		t.Fatalf("%v %v", f.Sections, err)
	}
	if f, err = svc.Export(context.Background(), []string{"plugin-settings"}); err != nil || f.Sections["plugin-settings"] == nil {
		t.Fatalf("%v %v", f.Sections, err)
	}
	if all, _ := svc.ExportAll(context.Background()); all.Sections["plugin-settings"] == nil || all.Sections["plugins"] == nil {
		t.Fatalf("%v", all.Sections)
	}
	if strings.Contains(string(f.Sections["plugin-settings"]), "empty") {
		t.Fatal("пустые настройки не экспортируются")
	}

	raw := mustRaw(t, settingsData{Plugins: map[string]json.RawMessage{
		"music": json.RawMessage(`{"reward":"y"}`), "new": json.RawMessage(`{"a":1}`), "empty": json.RawMessage(`{"a":1}`), "bad": json.RawMessage(`[1]`), "same": json.RawMessage(`{}`),
	}})
	h.loaded["bad"] = LoadedPlugin{}
	items, err := sec.Plan(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	got := statuses(items)
	if got["music"].Status != bundle.StatusReplace || got["new"].Status != bundle.StatusProblem || !strings.Contains(got["new"].Reason, "not installed") ||
		got["empty"].Status != bundle.StatusNew || got["bad"].Status != bundle.StatusProblem || !got["music"].Optional {
		t.Fatalf("%+v", got)
	}
	out, err := sec.Apply(context.Background(), raw, map[string]string{"music": bundle.ModeReplace, "new": bundle.ModeReplace})
	if err != nil || len(out) != 2 || !out[1].OK && out[1].Error == "" {
		t.Fatalf("%+v %v", out, err)
	}
	if string(h.configs["music"]) != `{"reward":"y"}` || out[0].ID != "music" || !out[0].OK || out[1].OK {
		t.Fatalf("%+v %v", out, h.configs)
	}
}
