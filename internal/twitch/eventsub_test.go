package twitch

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// fakeConn — поддельный сокет: сообщения кладутся в in, чтение учитывает дедлайн, Close будит читателя.
type fakeConn struct {
	in       chan string
	closed   chan struct{}
	once     sync.Once
	mu       sync.Mutex
	deadline time.Time
}

func newFakeConn() *fakeConn {
	return &fakeConn{in: make(chan string, 16), closed: make(chan struct{})}
}

func (c *fakeConn) ReadMessage() ([]byte, error) {
	c.mu.Lock()
	d := c.deadline
	c.mu.Unlock()
	var timeout <-chan time.Time
	if !d.IsZero() {
		timeout = time.After(time.Until(d))
	}
	select {
	case m := <-c.in:
		return []byte(m), nil
	case <-c.closed:
		return nil, errors.New("closed")
	case <-timeout:
		return nil, errors.New("i/o timeout")
	}
}
func (c *fakeConn) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	c.deadline = t
	c.mu.Unlock()
	return nil
}
func (c *fakeConn) Close() error { c.once.Do(func() { close(c.closed) }); return nil }
func (c *fakeConn) isClosed() bool {
	select {
	case <-c.closed:
		return true
	default:
		return false
	}
}

func welcome(id, session string, keepalive int) string {
	return fmt.Sprintf(`{"metadata":{"message_id":%q,"message_type":"session_welcome"},"payload":{"session":{"id":%q,"keepalive_timeout_seconds":%d}}}`, id, session, keepalive)
}
func redeemMsg(msgID, redID, input string) string {
	return fmt.Sprintf(`{"metadata":{"message_id":%q,"message_type":"notification"},"payload":{"subscription":{"type":%q},"event":{"id":%q,"user_id":"7","user_login":"viewer","user_name":"Viewer","user_input":%q,"status":"unfulfilled","reward":{"id":"mine","title":"Track"},"redeemed_at":"2026-01-02T03:04:05Z"}}}`, msgID, redemptionType, redID, input)
}
func msgOf(id, typ, payload string) string {
	return fmt.Sprintf(`{"metadata":{"message_id":%q,"message_type":%q},"payload":%s}`, id, typ, payload)
}

type esHarness struct {
	es    *EventSub
	conns map[string]*fakeConn
	dials []string
	got   chan Redemption
	mu    sync.Mutex
}

func newESHarness(t *testing.T, scopes ...string) (*esHarness, *helixFake) {
	t.Helper()
	m, f, _ := newRewardsManager(t, scopes...)
	h := &esHarness{conns: map[string]*fakeConn{"ws://main": newFakeConn(), "ws://moved": newFakeConn()}, got: make(chan Redemption, 8)}
	h.es = m.NewEventSub(func(r Redemption) { h.got <- r }, nil)
	h.es.UseURL("ws://main")
	h.es.dialer = func(_ context.Context, url string) (wsConn, error) {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.dials = append(h.dials, url)
		c, ok := h.conns[url]
		if !ok {
			return nil, errors.New("no such host")
		}
		return c, nil
	}
	return h, f
}

func (h *esHarness) next(t *testing.T) Redemption {
	t.Helper()
	select {
	case r := <-h.got:
		return r
	case <-time.After(2 * time.Second):
		t.Fatal("погашение не доставлено")
		return Redemption{}
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("не дождались: %s", what)
}

func TestEventSubSubscribesDeliversAndDedupes(t *testing.T) {
	h, f := newESHarness(t)
	main := h.conns["ws://main"]
	done := make(chan error, 1)
	go func() { _, err := h.es.session(context.Background()); done <- err }()

	main.in <- welcome("m1", "sess-1", 10)
	waitFor(t, "подписка", func() bool { return f.count() == 1 })
	c := f.last()
	cond := c.Body["condition"].(map[string]any)
	tr := c.Body["transport"].(map[string]any)
	if c.Path != "/helix/eventsub/subscriptions" || c.Body["type"] != redemptionType || cond["broadcaster_user_id"] != "42" ||
		tr["method"] != "websocket" || tr["session_id"] != "sess-1" || c.Auth != "Bearer tok" || c.ClientID != "cid" {
		t.Fatalf("подписка: %+v", c)
	}
	if c.Query != "" {
		t.Fatalf("у подписки не должно быть broadcaster_id в query: %q", c.Query)
	}

	main.in <- redeemMsg("m2", "red-1", "https://music.yandex.ru/album/1/track/2")
	r := h.next(t)
	if r.ID != "red-1" || r.RewardID != "mine" || r.UserLogin != "viewer" || r.Input != "https://music.yandex.ru/album/1/track/2" ||
		!r.RedeemedAt.Equal(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)) {
		t.Fatalf("%+v", r)
	}
	// тот же message_id повторно и сообщения других типов — не доставляются
	main.in <- redeemMsg("m2", "red-1", "x")
	main.in <- msgOf("m3", "session_keepalive", `{}`)
	main.in <- redeemMsg("m4", "red-2", "y")
	if r := h.next(t); r.ID != "red-2" {
		t.Fatalf("повтор message_id пропущен: %+v", r)
	}
	main.Close()
	if err := <-done; err == nil {
		t.Fatal("обрыв соединения должен вернуть ошибку")
	}
}

func TestEventSubReconnectKeepsSubscriptionAndSwapsSocket(t *testing.T) {
	h, f := newESHarness(t)
	main, moved := h.conns["ws://main"], h.conns["ws://moved"]
	go func() { _, _ = h.es.session(context.Background()) }()
	main.in <- welcome("m1", "sess-1", 10)
	waitFor(t, "подписка", func() bool { return f.count() == 1 })

	moved.in <- welcome("n1", "sess-1", 10) // после reconnect — welcome на новом адресе, подписки переносятся
	main.in <- msgOf("m2", "session_reconnect", `{"session":{"id":"sess-1","reconnect_url":"ws://moved"}}`)
	waitFor(t, "старый сокет закрыт", main.isClosed)
	moved.in <- redeemMsg("n2", "red-9", "z")
	if r := h.next(t); r.ID != "red-9" {
		t.Fatalf("%+v", r)
	}
	if f.count() != 1 {
		t.Fatalf("после reconnect заново подписываться не нужно, запросов: %d", f.count())
	}
	if len(h.dials) != 2 || h.dials[1] != "ws://moved" {
		t.Fatalf("адреса: %v", h.dials)
	}
	moved.Close()
}

func TestEventSubSadPaths(t *testing.T) {
	t.Run("тишина дольше keepalive — разрыв", func(t *testing.T) {
		h, _ := newESHarness(t)
		h.es.grace = 50 * time.Millisecond
		h.conns["ws://main"].in <- welcome("m1", "s", 1) // 1 с + 50 мс без сообщений
		start := time.Now()
		_, err := h.es.session(context.Background())
		if err == nil || time.Since(start) > 3*time.Second {
			t.Fatalf("ожидался разрыв по таймауту: %v (%v)", err, time.Since(start))
		}
	})
	t.Run("подписка отозвана", func(t *testing.T) {
		h, _ := newESHarness(t)
		main := h.conns["ws://main"]
		main.in <- welcome("m1", "s", 10)
		main.in <- msgOf("m2", "revocation", `{"subscription":{"type":"x","status":"authorization_revoked"}}`)
		if _, err := h.es.session(context.Background()); err == nil {
			t.Fatal("ожидалась ошибка")
		}
	})
	t.Run("нет прав — сокет даже не открываем", func(t *testing.T) {
		h, f := newESHarness(t, "chat:read")
		_, err := h.es.session(context.Background())
		if !errors.Is(err, ErrNeedScopes) || len(h.dials) != 0 || f.count() != 0 {
			t.Fatalf("%v dials=%v calls=%d", err, h.dials, f.count())
		}
	})
	t.Run("reconnect без адреса или на недоступный адрес", func(t *testing.T) {
		for _, payload := range []string{`{"session":{"id":"s"}}`, `{"session":{"id":"s","reconnect_url":"ws://nowhere"}}`} {
			h, _ := newESHarness(t)
			main := h.conns["ws://main"]
			main.in <- welcome("m1", "s", 10)
			main.in <- msgOf("m2", "session_reconnect", payload)
			if _, err := h.es.session(context.Background()); err == nil {
				t.Fatalf("%s: ожидалась ошибка", payload)
			}
		}
	})
	t.Run("битое погашение и чужие подписки пропускаются", func(t *testing.T) {
		h, _ := newESHarness(t)
		main := h.conns["ws://main"]
		main.in <- welcome("m1", "s", 10)
		main.in <- msgOf("m2", "notification", `{"subscription":{"type":"channel.follow"},"event":{"id":"x"}}`)
		main.in <- msgOf("m3", "notification", fmt.Sprintf(`{"subscription":{"type":%q},"event":{"id":""}}`, redemptionType))
		main.in <- `not json`
		main.in <- redeemMsg("m4", "ok", "")
		go func() { _, _ = h.es.session(context.Background()) }()
		if r := h.next(t); r.ID != "ok" {
			t.Fatalf("%+v", r)
		}
		main.Close()
	})
}

func TestEventSubRunReportsStatesAndStops(t *testing.T) {
	h, _ := newESHarness(t, "chat:read")
	var mu sync.Mutex
	var states []string
	h.es.onState = func(s, _ string) { mu.Lock(); states = append(states, s); mu.Unlock() }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.es.Run(ctx); close(done) }()
	waitFor(t, "unavailable", func() bool {
		mu.Lock()
		defer mu.Unlock()
		for _, s := range states {
			if s == ESUnavailable {
				return true
			}
		}
		return false
	})
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run не остановился после отмены контекста")
	}
}
