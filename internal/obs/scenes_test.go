package obs

import (
	"reflect"
	"testing"
)

func scene(name string, idx float64) any {
	return map[string]any{"sceneName": name, "sceneIndex": idx}
}

func TestScenesFollowTheOrderInOBS(t *testing.T) {
	// obs-websocket отдаёт массив в обратном порядке: сверху в OBS — индекс 0
	got := scenesInUIOrder(map[string]any{"scenes": []any{scene("C", 2), scene("B", 1), scene("A", 0)}})
	if !reflect.DeepEqual(got, []string{"A", "B", "C"}) {
		t.Fatalf("%v", got)
	}
	// без индексов массив переворачивается; записи без имени пропускаются
	got = scenesInUIOrder(map[string]any{"scenes": []any{map[string]any{"sceneName": "C"}, map[string]any{"sceneName": ""}, map[string]any{"sceneName": "A"}}})
	if !reflect.DeepEqual(got, []string{"A", "C"}) {
		t.Fatalf("%v", got)
	}
	if got = scenesInUIOrder(map[string]any{}); len(got) != 0 {
		t.Fatalf("%v", got)
	}
}
