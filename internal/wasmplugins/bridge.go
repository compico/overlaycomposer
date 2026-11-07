package wasmplugins

import (
	"context"
	"log/slog"
	"sync"
)

// Runner — источник событий, который работает, пока жив контекст (EventSub Twitch).
type Runner interface{ Run(ctx context.Context) }

const bridgeSeen = 256

// Bridge держит источник событий включённым, пока есть плагин, готовый их принимать, и выключает, когда такого нет:
// без подписчика соединение с Twitch не нужно. Повторные доставки одного id (после переподключения) отсекаются.
type Bridge struct {
	newRunner func(deliver func(id string, data any), onState func(state, detail string)) Runner
	publish   func(typ string, data any) int

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
	state  string
	detail string
	seen   map[string]bool
	ring   []string
}

func NewBridge(newRunner func(deliver func(id string, data any), onState func(state, detail string)) Runner, publish func(typ string, data any) int) *Bridge {
	return &Bridge{newRunner: newRunner, publish: publish, state: "off", seen: map[string]bool{}}
}

// State — состояние источника для индикатора: "off", когда он не нужен или остановлен.
func (b *Bridge) State() (string, string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state, b.detail
}

func (b *Bridge) onState(state, detail string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.cancel == nil { // источник уже остановлен: его запоздалое «disconnected» не должно перекрыть «off»
		return
	}
	b.state, b.detail = state, detail
}

func (b *Bridge) deliver(id string, data any) {
	b.mu.Lock()
	dup := id != "" && b.seen[id]
	if id != "" && !dup {
		b.seen[id] = true
		b.ring = append(b.ring, id)
		if len(b.ring) > bridgeSeen {
			delete(b.seen, b.ring[0])
			b.ring = b.ring[1:]
		}
	}
	b.mu.Unlock()
	if dup {
		return
	}
	if n := b.publish(EventRedemption, data); n == 0 {
		slog.Warn("twitch: погашение никому не доставлено (нет подписанного плагина с разрешением)", "id", id)
	}
}

// Sync включает или выключает источник в соответствии с тем, нужен ли он плагинам. Повторный вызов с тем же значением безвреден.
func (b *Bridge) Sync(wanted bool) {
	b.mu.Lock()
	running := b.cancel != nil
	switch {
	case wanted && !running:
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		b.cancel, b.done = cancel, done
		b.state, b.detail = "connecting", ""
		r := b.newRunner(b.deliver, b.onState)
		go func() {
			defer close(done)
			r.Run(ctx)
		}()
		b.mu.Unlock()
	case !wanted && running:
		cancel, done := b.cancel, b.done
		b.cancel, b.done = nil, nil
		b.state, b.detail = "off", ""
		b.mu.Unlock()
		cancel()
		<-done
	default:
		b.mu.Unlock()
	}
}

func (b *Bridge) Close() { b.Sync(false) }
