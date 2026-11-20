package content

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/compico/overlaycomposer/internal/bundle"
	"github.com/compico/overlaycomposer/internal/market"
)

// PluginHost — то, что секциям плагинов нужно от магазина и менеджера wasm-плагинов.
type PluginHost interface {
	Lock() market.WasmLockFile
	Plan(ctx context.Context, f market.WasmLockFile) []market.WasmLockPlanItem
	// Install ставит ровно записанный релиз (тег и sha256 из lock): файл сверяется с lock, а не с текущим релизом.
	Install(ctx context.Context, repo, id, tag, sha string) error
	// Loaded — загруженный плагин: sha256 файла, что он запрашивает, что выдано, его настройки.
	Loaded(id string) (LoadedPlugin, bool)
	LoadedIDs() []string
	SetGrants(id string, perms []string) error
	SetConfig(id string, cfg json.RawMessage) error
}

// LoadedPlugin — состояние загруженного плагина.
type LoadedPlugin struct {
	SHA256    string
	Requested []string
	Granted   []string
	Config    json.RawMessage
}

// ---- plugins: lock + права ----

type pluginsData struct {
	Lock   market.WasmLockFile `json:"lock"`
	Grants map[string][]string `json:"grants,omitempty"` // выданные права по id (применяются только если sha256 совпал с lock)
}

// Plugins — секция «plugins»: какие автономные плагины стоят (lock) и какие права им выданы. Настройки плагинов — в «plugin-settings».
type Plugins struct{ Host PluginHost }

func (*Plugins) Name() string { return "plugins" }

func (p *Plugins) Export(context.Context) (json.RawMessage, error) {
	lock := p.Host.Lock()
	out := pluginsData{Lock: lock, Grants: map[string][]string{}}
	for _, e := range lock.Plugins {
		if lp, ok := p.Host.Loaded(e.ID); ok && len(lp.Granted) > 0 {
			g := append([]string(nil), lp.Granted...)
			sort.Strings(g)
			out.Grants[e.ID] = g
		}
	}
	return json.Marshal(out)
}

func (p *Plugins) parse(raw json.RawMessage) (pluginsData, error) {
	var in pluginsData
	if err := json.Unmarshal(raw, &in); err != nil {
		return in, errors.New("the plugins section is unreadable")
	}
	return in, nil
}

func (p *Plugins) Plan(ctx context.Context, raw json.RawMessage) ([]bundle.Item, error) {
	in, err := p.parse(raw)
	if err != nil {
		return nil, err
	}
	var items []bundle.Item
	for _, pi := range p.Host.Plan(ctx, in.Lock) {
		it := bundle.Item{ID: pi.ID, Name: pi.ID + " " + pi.Version, Reason: pi.Reason}
		switch pi.Status {
		case market.LockOK:
			it.Status = bundle.StatusNew
		case market.LockInstalled:
			it.Status = bundle.StatusSame
		case market.LockDiffers:
			it.Status, it.Warning = bundle.StatusReplace, true
		default:
			it.Status = bundle.StatusProblem
			if pi.Deprecation != nil && pi.Deprecation.ReplacedBy != nil {
				it.Reason += " Replacement: " + pi.Deprecation.ReplacedBy.ID + " (" + pi.Deprecation.ReplacedBy.Repo + ")."
			}
		}
		if it.Status == bundle.StatusNew || it.Status == bundle.StatusReplace {
			if len(in.Grants[pi.ID]) > 0 {
				it.Reason = strings.TrimSpace(it.Reason + " Saved permissions (" + strings.Join(in.Grants[pi.ID], ", ") + ") are granted only because the downloaded file matches the lock.")
			}
		}
		items = append(items, it)
	}
	return items, nil
}

func (p *Plugins) Apply(ctx context.Context, raw json.RawMessage, pick map[string]string) ([]bundle.Outcome, error) {
	in, err := p.parse(raw)
	if err != nil {
		return nil, err
	}
	var out []bundle.Outcome
	for _, e := range in.Lock.Plugins {
		mode, ok := pick[e.ID]
		if !ok {
			continue
		}
		o := bundle.Outcome{ID: e.ID}
		if mode != bundle.ModeReplace {
			o.Error = "plugins are imported by replacing only"
			out = append(out, o)
			continue
		}
		// install проверяет файл по sha256 из lock; права ниже выдаются только если установленный файл ровно тот
		if err := p.Host.Install(ctx, e.Repo, e.ID, e.Tag, strings.ToLower(e.Sha256)); err != nil {
			o.Error = err.Error()
			out = append(out, o)
			continue
		}
		o.OK = true
		o.Note = p.restoreGrants(e, in.Grants[e.ID])
		out = append(out, o)
	}
	return out, nil
}

// restoreGrants выдаёт сохранённые права, если sha256 загруженного плагина совпал с lock; иначе — ничего (пользователь подтвердит заново).
func (p *Plugins) restoreGrants(e market.WasmLockEntry, saved []string) string {
	lp, ok := p.Host.Loaded(e.ID)
	if !ok || !strings.EqualFold(lp.SHA256, e.Sha256) {
		return "Permissions were not restored (the plugin file does not match the lock): grant them again in Plugins."
	}
	want := map[string]bool{}
	for _, r := range lp.Requested {
		want[r] = true
	}
	var give []string
	for _, g := range saved {
		if want[g] {
			give = append(give, g)
		}
	}
	if len(give) > 0 {
		if err := p.Host.SetGrants(e.ID, give); err != nil {
			return "Permissions were not restored: " + err.Error()
		}
	}
	had := map[string]bool{}
	for _, g := range give {
		had[g] = true
	}
	var pending []string
	for _, r := range lp.Requested {
		if !had[r] {
			pending = append(pending, r)
		}
	}
	sort.Strings(pending)
	note := ""
	if len(give) > 0 {
		note = "Restored permissions: " + strings.Join(give, ", ") + "."
	}
	if len(pending) > 0 {
		note = strings.TrimSpace(note + " Not granted yet (confirm in Plugins): " + strings.Join(pending, ", ") + ".")
	}
	return note
}

// ---- plugin-settings: настройки плагинов (необязательная секция) ----

type settingsData struct {
	Plugins map[string]json.RawMessage `json:"plugins"`
}

// PluginSettingsSection — секция «plugin-settings»: настройки (config) загруженных плагинов. Может содержать токены и адреса,
// поэтому по умолчанию в файл не попадает: только если выбрана явно (см. bundle.OptIn). Плагин должен быть уже установлен.
type PluginSettingsSection struct{ Host PluginHost }

func (*PluginSettingsSection) Name() string { return "plugin-settings" }
func (*PluginSettingsSection) OptIn() bool  { return true }

const maxPluginConfig = 64 << 10

func (p *PluginSettingsSection) Export(context.Context) (json.RawMessage, error) {
	out := settingsData{Plugins: map[string]json.RawMessage{}}
	for _, id := range p.Host.LoadedIDs() {
		if lp, ok := p.Host.Loaded(id); ok && len(bytes.TrimSpace(lp.Config)) > 2 {
			out.Plugins[id] = lp.Config
		}
	}
	return json.Marshal(out)
}

func (p *PluginSettingsSection) parse(raw json.RawMessage) (map[string]json.RawMessage, error) {
	var in settingsData
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, errors.New("the plugin-settings section is unreadable")
	}
	if len(in.Plugins) > maxItems {
		return nil, errors.New("too many plugins")
	}
	return in.Plugins, nil
}

func sortedKeys(m map[string]json.RawMessage) []string {
	k := make([]string, 0, len(m))
	for id := range m {
		k = append(k, id)
	}
	sort.Strings(k)
	return k
}

func checkConfig(raw json.RawMessage) error {
	if len(raw) > maxPluginConfig {
		return fmt.Errorf("settings are larger than %d KB", maxPluginConfig>>10)
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return errors.New("settings must be a JSON object")
	}
	return nil
}

func (p *PluginSettingsSection) Plan(_ context.Context, raw json.RawMessage) ([]bundle.Item, error) {
	in, err := p.parse(raw)
	if err != nil {
		return nil, err
	}
	var items []bundle.Item
	for _, id := range sortedKeys(in) {
		it := bundle.Item{ID: id, Name: id + " settings", Optional: true}
		lp, loaded := p.Host.Loaded(id)
		switch {
		case checkConfig(in[id]) != nil:
			it.Status, it.Reason = bundle.StatusProblem, checkConfig(in[id]).Error()
		case !loaded:
			it.Status, it.Reason = bundle.StatusProblem, "The plugin is not installed. Install it first (import the plugins section), then import its settings."
		case canonEqualRaw(lp.Config, in[id]):
			it.Status = bundle.StatusSame
		case len(bytes.TrimSpace(lp.Config)) <= 2:
			it.Status = bundle.StatusNew
		default:
			it.Status, it.Reason, it.Warning = bundle.StatusReplace, "Replaces the plugin's current settings and restarts it.", true
		}
		items = append(items, it)
	}
	return items, nil
}

func canonEqualRaw(a, b json.RawMessage) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	return canonEqual(x, y)
}

func (p *PluginSettingsSection) Apply(_ context.Context, raw json.RawMessage, pick map[string]string) ([]bundle.Outcome, error) {
	in, err := p.parse(raw)
	if err != nil {
		return nil, err
	}
	var out []bundle.Outcome
	for _, id := range sortedKeys(in) {
		mode, ok := pick[id]
		if !ok {
			continue
		}
		o := bundle.Outcome{ID: id}
		switch {
		case mode != bundle.ModeReplace:
			o.Error = "plugin settings are imported by replacing only"
		case checkConfig(in[id]) != nil:
			o.Error = checkConfig(in[id]).Error()
		default:
			if err := p.Host.SetConfig(id, in[id]); err != nil {
				o.Error = err.Error()
			} else {
				o.OK = true
			}
		}
		out = append(out, o)
	}
	return out, nil
}
