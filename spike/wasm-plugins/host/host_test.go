package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
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

// Сценарий заявки целиком, как его увидит аддон плеера: событие Twitch → GET /next → POST /started → FULFILLED.
// То же, что ручные curl, но без терминала.
func TestMusicFlowOverHTTP(t *testing.T) {
	p := loadWasm(t, "../music/music.wasm", `{"rewardId":"rw"}`)
	srv := httptest.NewServer(p.routes())
	defer srv.Close()
	do := func(method, path, body string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	updates := func() []string { p.updMu.Lock(); defer p.updMu.Unlock(); return append([]string(nil), p.updates...) }

	ev := func(id, input string) string {
		return fmt.Sprintf(`{"type":"twitch.redemption","data":{"id":%q,"rewardId":"rw","userLogin":"v","input":%q}}`, id, input)
	}
	if code, _ := do("POST", "/dev/event", ev("r1", "https://music.yandex.ru/album/1/track/2")); code != 202 {
		t.Fatalf("событие: %d", code)
	}
	if code, body := do("GET", "/plugins/music/next", ""); code != 200 || !strings.Contains(body, `"id":1`) || !strings.Contains(body, `"trackId":"2"`) {
		t.Fatalf("next: %d %s", code, body)
	}
	if code, _ := do("POST", "/plugins/music/started", `{"id":1}`); code != 200 {
		t.Fatalf("started: %d", code)
	}
	if u := updates(); len(u) != 1 || u[0] != "r1:FULFILLED" {
		t.Fatalf("погашение должно закрыться: %v", u)
	}

	// sad path: плохая ссылка возвращает баллы, чужой id, мусор, неизвестные маршруты
	do("POST", "/dev/event", ev("r2", "https://evil.example/x"))
	if u := updates(); len(u) != 2 || u[1] != "r2:CANCELED" {
		t.Fatalf("плохая ссылка → CANCELED: %v", u)
	}
	if code, _ := do("POST", "/plugins/music/started", `{"id":99}`); code != 404 {
		t.Fatalf("неизвестная заявка: %d", code)
	}
	if code, _ := do("POST", "/plugins/music/started", `не json`); code != 400 {
		t.Fatalf("мусорное тело: %d", code)
	}
	if code, _ := do("POST", "/dev/event", `{}`); code != 400 {
		t.Fatalf("событие без типа: %d", code)
	}
	if code, _ := do("GET", "/plugins/music/nope", ""); code != 404 {
		t.Fatalf("неизвестный маршрут: %d", code)
	}
	if code, _ := do("GET", "/plugins/music/started", ""); code != 405 {
		t.Fatalf("неверный метод: %d", code)
	}
}
