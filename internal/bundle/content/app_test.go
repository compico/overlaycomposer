package content

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/compico/overlaycomposer/internal/bundle"
)

type fakeSources struct {
	have []string
	fail map[string]bool
}

func (f *fakeSources) Sources() []string { return f.have }
func (f *fakeSources) AddSource(_ context.Context, repo string) (string, error) {
	if f.fail[repo] {
		return "", errors.New("no plugins found")
	}
	f.have = append(f.have, repo)
	return repo, nil
}

func TestStoreExportPlanApply(t *testing.T) {
	src := &fakeSources{have: []string{"compico/oc-plugins", "o/n"}}
	raw, err := (&Store{List: src}).Export(context.Background())
	if err != nil || !strings.Contains(string(raw), `"compico/oc-plugins"`) {
		t.Fatalf("%s %v", raw, err)
	}
	dst := &fakeSources{have: []string{"Compico/OC-Plugins"}, fail: map[string]bool{"bad/repo": true}}
	sec := &Store{List: dst}
	in := mustRaw(t, storeData{Sources: []string{"compico/oc-plugins", "o/n", "O/N", "not a repo", "bad/repo"}})
	items, err := sec.Plan(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if items[0].Status != bundle.StatusSame || items[1].Status != bundle.StatusNew || !items[1].Warning || !items[1].Optional ||
		items[2].Status != bundle.StatusProblem || !strings.Contains(items[2].Reason, "twice") || items[3].Status != bundle.StatusProblem {
		t.Fatalf("%+v", items)
	}
	out, err := sec.Apply(context.Background(), in, map[string]string{"o/n": bundle.ModeReplace, "bad/repo": bundle.ModeReplace})
	if err != nil || len(out) != 2 || !out[0].OK || out[1].OK || out[1].Error == "" {
		t.Fatalf("%+v %v", out, err)
	}
	if len(dst.have) != 2 || dst.have[1] != "o/n" {
		t.Fatalf("%v", dst.have)
	}
}

type fakeObs struct {
	cur, applied ObsSettings
	err          error
}

func (f *fakeObs) Get() ObsSettings { return f.cur }
func (f *fakeObs) Apply(o ObsSettings) error {
	if f.err != nil {
		return f.err
	}
	f.applied, f.cur = o, o
	return nil
}

func TestAppObs(t *testing.T) {
	src := &fakeObs{cur: ObsSettings{Enabled: true, URL: "ws://192.168.1.5:4455"}}
	raw, _ := (&App{Obs: src}).Export(context.Background())
	if strings.Contains(string(raw), "assword") {
		t.Fatalf("пароль в файле: %s", raw)
	}
	dst := &fakeObs{cur: ObsSettings{Enabled: true, URL: "ws://127.0.0.1:4455"}}
	sec := &App{Obs: dst}
	items, _ := sec.Plan(context.Background(), raw)
	if len(items) != 1 || items[0].Status != bundle.StatusReplace || !items[0].Optional || !strings.Contains(items[0].Reason, "password") {
		t.Fatalf("%+v", items)
	}
	if out, _ := sec.Apply(context.Background(), raw, map[string]string{ObsItemID: bundle.ModeReplace}); len(out) != 1 || !out[0].OK || dst.applied.URL != "ws://192.168.1.5:4455" {
		t.Fatalf("%+v %+v", out, dst)
	}
	if items, _ = sec.Plan(context.Background(), raw); items[0].Status != bundle.StatusSame {
		t.Fatalf("%+v", items)
	}
	bad := []byte(`{"obs":{"enabled":true,"url":"http://x"}}`)
	if items, _ = sec.Plan(context.Background(), bad); items[0].Status != bundle.StatusProblem {
		t.Fatalf("%+v", items)
	}
	if out, _ := sec.Apply(context.Background(), bad, map[string]string{ObsItemID: bundle.ModeReplace}); out[0].OK {
		t.Fatalf("%+v", out)
	}
	if items, _ = sec.Plan(context.Background(), []byte(`{}`)); len(items) != 0 {
		t.Fatalf("%+v", items)
	}
}
