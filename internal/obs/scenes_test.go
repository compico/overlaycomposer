package obs

import (
	"reflect"
	"testing"
)

func scene(name string, idx float64) any {
	return map[string]any{"sceneName": name, "sceneIndex": idx}
}

func TestScenesFollowTheOrderInOBS(t *testing.T) {
	// по наблюдению на живом OBS массив идёт по возрастанию sceneIndex (0 — нижняя сцена), а в OBS сверху — последняя: сортируем по убыванию
	got := scenesInUIOrder(map[string]any{"scenes": []any{scene("A", 0), scene("B", 1), scene("C", 2)}})
	if !reflect.DeepEqual(got, []string{"C", "B", "A"}) {
		t.Fatalf("%v", got)
	}
	// порядок массива не важен, важны индексы
	got = scenesInUIOrder(map[string]any{"scenes": []any{scene("B", 1), scene("C", 2), scene("A", 0)}})
	if !reflect.DeepEqual(got, []string{"C", "B", "A"}) {
		t.Fatalf("%v", got)
	}
	// без индексов — порядок массива; записи без имени пропускаются
	got = scenesInUIOrder(map[string]any{"scenes": []any{map[string]any{"sceneName": "C"}, map[string]any{"sceneName": ""}, map[string]any{"sceneName": "A"}}})
	if !reflect.DeepEqual(got, []string{"C", "A"}) {
		t.Fatalf("%v", got)
	}
	if got = scenesInUIOrder(map[string]any{}); len(got) != 0 {
		t.Fatalf("%v", got)
	}
	if got := rawScenes(map[string]any{"scenes": []any{scene("A", 0), scene("B", 1)}}); !reflect.DeepEqual(got, []string{"A=0", "B=1"}) {
		t.Fatalf("%v", got)
	}
}
