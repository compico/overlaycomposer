package wasmplugins

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
)

type fakeRunner struct {
	started, stopped *atomic.Int32
	ready            chan struct{}
	onState          func(string, string)
}

func (r *fakeRunner) Run(ctx context.Context) {
	r.started.Add(1)
	r.onState("connected", "")
	close(r.ready)
	<-ctx.Done()
	r.onState("disconnected", "") // реальный EventSub тоже сообщает об остановке
	r.stopped.Add(1)
}

func TestBridgeStartsStopsAndDedups(t *testing.T) {
	var started, stopped atomic.Int32
	var deliver func(string, any)
	ready := make(chan struct{})
	var mu sync.Mutex
	var got []string
	delivered := 1 // сколько плагинов «приняло» событие
	b := NewBridge(func(d func(string, any), onState func(string, string)) Runner {
		deliver = d
		return &fakeRunner{started: &started, stopped: &stopped, ready: ready, onState: onState}
	}, func(typ string, data any) int {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, typ+":"+data.(string))
		return delivered
	})
	if st, _ := b.State(); st != "off" {
		t.Fatalf("initial state %q", st)
	}
	b.Sync(false) // нечего выключать
	b.Sync(true)
	b.Sync(true) // повторное включение не запускает второй раз
	<-ready
	if st, _ := b.State(); st != "connected" {
		t.Fatalf("state %q", st)
	}
	deliver("r1", "a")
	deliver("r1", "a-again") // тот же id после переподключения
	deliver("r2", "b")
	deliver("", "no-id") // без id отсечь нечем — доставляем
	deliver("", "no-id")
	mu.Lock()
	if len(got) != 4 || got[0] != "twitch.redemption:a" || got[1] != "twitch.redemption:b" {
		t.Fatalf("delivered: %v", got)
	}
	mu.Unlock()
	delivered = 0 // sad path: никто не принял — только предупреждение в лог, без паники
	deliver("r3", "c")

	b.Sync(false)
	if started.Load() != 1 || stopped.Load() != 1 {
		t.Fatalf("started=%d stopped=%d", started.Load(), stopped.Load())
	}
	if st, _ := b.State(); st != "off" {
		t.Fatalf("after stop the state must be off, got %q", st)
	}
	b.Close() // повторная остановка безвредна
}
