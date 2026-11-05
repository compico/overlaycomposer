package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"
)

func loadWasm(t *testing.T, path, cfg string) *plugin {
	t.Helper()
	wasm, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("нет %s: соберите плагин (см. main.go)", path)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	p, err := load(ctx, wasm, json.RawMessage(cfg))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// Параллельность: сотни одновременных вызовов маршрутов, событий и таймеров к одному плагину — без гонок и потерь.
// Запускать с -race.
func TestConcurrentCallsAreSerialisedWithoutLoss(t *testing.T) {
	p := loadWasm(t, "../music/music.wasm", `{"rewardId":"rw"}`)
	ctx := context.Background()
	const n = 100
	var wg sync.WaitGroup
	for i := 1; i <= n; i++ {
		wg.Add(3)
		go func(i int) {
			defer wg.Done()
			ev := map[string]any{"id": fmt.Sprintf("r%d", i), "rewardId": "rw", "userLogin": "v", "input": fmt.Sprintf("https://music.yandex.ru/album/%d/track/%d", i, i)}
			if err := p.deliver(ctx, "twitch.redemption", ev); err != nil {
				t.Error(err)
			}
		}(i)
		go func() {
			defer wg.Done()
			if _, err := p.call(ctx, "queue", request{Method: "GET"}); err != nil {
				t.Error(err)
			}
		}()
		go func() {
			defer wg.Done()
			if err := p.deliver(ctx, "timer", map[string]string{"id": "tick"}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	resp, err := p.call(ctx, "queue", request{Method: "GET"})
	if err != nil {
		t.Fatal(err)
	}
	if items, _ := resp.Body["items"].([]any); len(items) != n {
		t.Fatalf("заявок %d, ожидалось %d: события потерялись или перемешались", len(items), n)
	}
}

// Падение плагина не теряет состояние и не мешает следующим вызовам (в hello есть обработчик boom).
func TestTrapRestartsAndKeepsHostState(t *testing.T) {
	p := loadWasm(t, "../guest/hello.wasm", `{}`)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if _, err := p.call(ctx, "greet", request{Method: "GET", Query: map[string]string{"name": "A"}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := p.call(ctx, "boom", request{Method: "GET"}); err == nil {
		t.Fatal("boom должен вернуть ошибку")
	}
	resp, err := p.call(ctx, "greet", request{Method: "GET", Query: map[string]string{"name": "A"}})
	if err != nil || resp.Body["count"] != float64(3) {
		t.Fatalf("после перезапуска счётчик должен продолжиться: %v %+v", err, resp)
	}
}

// Таймер, заказанный плагином в oc_init, срабатывает и переживает перезапуск экземпляра (таймер хоста, а не плагина).
func TestTimerFromInitFires(t *testing.T) {
	p := loadWasm(t, "../guest/hello.wasm", `{}`)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if resp, err := p.call(context.Background(), "ticks", request{Method: "GET"}); err == nil && resp.Body["ticks"].(float64) >= 2 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("таймер не сработал дважды за 3 секунды")
}
