package web

import (
	"strings"
	"testing"
)

func TestPagesEmbedded(t *testing.T) {
	for name, load := range map[string]func() ([]byte, error){"editor": EditorHTML, "overlay": OverlayHTML, "dashboard": DashboardHTML} {
		b, err := load()
		if err != nil || len(b) == 0 {
			t.Fatalf("%s: %v", name, err)
		}
		// Страница грузит одноимённый бандл, который собирает cmd/build.
		bundle := "/dist/" + map[string]string{"editor": "editor", "overlay": "runtime", "dashboard": "dashboard"}[name] + ".js"
		if !strings.Contains(string(b), bundle) {
			t.Errorf("%s: нет ссылки на %s", name, bundle)
		}
	}
}
