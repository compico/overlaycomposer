package wasmplugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestHandleRoutesAndResponses(t *testing.T) {
	meta := baseMeta("greeter")
	meta.Routes = []Route{{"GET", "/greet", "greet"}, {"POST", "/empty", "empty"}, {"GET", "/bad", "bad"}, {"GET", "/st", "st"}}
	mod := &fakeModule{meta: meta, handler: func(in *fakeInst, name string, arg []byte) ([]byte, error) {
		switch name {
		case "greet":
			var r Request
			_ = json.Unmarshal(arg, &r)
			return []byte(`{"body":{"hello":"` + r.Query["name"] + `"}}`), nil
		case "empty":
			return nil, nil
		case "st":
			return []byte(`{"status":999,"body":1}`), nil
		}
		return []byte(`not json`), nil
	}}
	p := newTestPlugin(t, mod, Services{})
	ctx := context.Background()

	res, err := p.Handle(ctx, Request{Method: "GET", Path: "/greet", Query: map[string]string{"name": "oc"}})
	if err != nil || res.Status != 200 || string(res.Body) != `{"hello":"oc"}` {
		t.Fatalf("greet: %v %+v", err, res)
	}
	if res, err = p.Handle(ctx, Request{Method: "POST", Path: "/empty"}); err != nil || res.Status != 204 {
		t.Fatalf("empty: %v %+v", err, res)
	}
	// sad path
	if _, err = p.Handle(ctx, Request{Method: "POST", Path: "/greet"}); !errors.Is(err, ErrNoRoute) {
		t.Fatalf("wrong method must be ErrNoRoute, got %v", err)
	}
	if _, err = p.Handle(ctx, Request{Method: "GET", Path: "/bad"}); err == nil {
		t.Fatal("invalid JSON from plugin must be an error")
	}
	if _, err = p.Handle(ctx, Request{Method: "GET", Path: "/st"}); err == nil {
		t.Fatal("status 999 must be rejected")
	}
	if p.Info().State != StateRunning {
		t.Fatal("a bad response is not a crash")
	}
}

func TestCrashRestartKeepsStorageAndLoopGuard(t *testing.T) {
	meta := baseMeta("crashy")
	meta.Permissions = []string{PermStorage}
	meta.Routes = []Route{{"GET", "/inc", "inc"}, {"GET", "/boom", "boom"}}
	store := &memStorage{}
	mod := &fakeModule{meta: meta, handler: func(in *fakeInst, name string, arg []byte) ([]byte, error) {
		if name == "boom" {
			return nil, errors.New("wasm trap: unreachable")
		}
		cur := in.hc("storage.get", map[string]string{"key": "n"})
		n, _ := cur["value"].(float64)
		in.hc("storage.set", map[string]any{"key": "n", "value": n + 1})
		return []byte(`{"body":{"n":` + strings.TrimSuffix(strings.TrimSuffix(jsonNum(n+1), ".0"), "") + `}}`), nil
	}}
	p := newTestPlugin(t, mod, Services{Storage: store}, PermStorage)
	ctx := context.Background()
	inc := func() string {
		r, err := p.Handle(ctx, Request{Method: "GET", Path: "/inc"})
		if err != nil {
			t.Fatal(err)
		}
		return string(r.Body)
	}
	inc()
	if got := inc(); got != `{"n":2}` {
		t.Fatalf("counter = %s", got)
	}
	if _, err := p.Handle(ctx, Request{Method: "GET", Path: "/boom"}); err == nil {
		t.Fatal("trap must surface as an error")
	}
	if mod.count() != 2 {
		t.Fatalf("instance must be recreated after a trap, instances=%d", mod.count())
	}
	if got := inc(); got != `{"n":3}` {
		t.Fatalf("state must survive the restart, got %s", got)
	}
	if p.Info().Restarts != 1 {
		t.Fatalf("restarts = %d", p.Info().Restarts)
	}
	// sad path: падает снова и снова → failed, дальше маршруты недоступны
	for i := 0; i < crashLimit; i++ {
		_, _ = p.Handle(ctx, Request{Method: "GET", Path: "/boom"})
	}
	if st := p.Info(); st.State != StateFailed || st.LastError == "" {
		t.Fatalf("crash loop must end in failed with a reason: %+v", st)
	}
	if _, err := p.Handle(ctx, Request{Method: "GET", Path: "/inc"}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("failed plugin must be unavailable, got %v", err)
	}
	if err := p.ResetFailure(); err != nil || p.Info().State != StateRunning {
		t.Fatalf("manual restart must recover: %v %+v", err, p.Info())
	}
}

func jsonNum(f float64) string { b, _ := json.Marshal(f); return string(b) }

func TestPermissionsDenyUntilGranted(t *testing.T) {
	meta := baseMeta("perm")
	meta.Permissions = []string{PermStorage, PermTimer}
	var denied, allowed atomic.Value
	mod := &fakeModule{meta: meta, handler: func(in *fakeInst, name string, arg []byte) ([]byte, error) {
		r := in.hc("storage.set", map[string]any{"key": "k", "value": 1})
		if name == "first" {
			denied.Store(r)
		} else {
			allowed.Store(r)
		}
		return nil, nil
	}}
	meta.Routes = []Route{{"GET", "/first", "first"}, {"GET", "/second", "second"}}
	mod.meta = meta
	store := &memStorage{}
	p := newTestPlugin(t, mod, Services{Storage: store})
	ctx := context.Background()
	_, _ = p.Handle(ctx, Request{Method: "GET", Path: "/first"})
	if r, _ := denied.Load().(map[string]any); r == nil || !strings.Contains(r["error"].(string), "permission denied") {
		t.Fatalf("storage without grant must be denied: %v", r)
	}
	if _, ok, _ := store.Get(ctx, "perm", "k"); ok {
		t.Fatal("denied write must not reach the storage")
	}
	p.setGrants([]string{PermStorage, PermNetHTTP}) // net.http не запрашивался — отфильтруется
	if p.granted(PermNetHTTP) {
		t.Fatal("a permission the plugin did not request must not be grantable")
	}
	_, _ = p.Handle(ctx, Request{Method: "GET", Path: "/second"})
	if r, _ := allowed.Load().(map[string]any); r == nil || r["error"] != nil {
		t.Fatalf("granted storage must work: %v", r)
	}
	if _, ok, _ := store.Get(ctx, "perm", "k"); !ok {
		t.Fatal("write must reach the storage")
	}
}

func TestEventsRedemptionAndTimer(t *testing.T) {
	meta := baseMeta("ev")
	meta.Permissions = []string{PermRedeem, PermTimer}
	meta.Subscriptions = []string{EventRedemption}
	var mu sync.Mutex
	var got []string
	mod := &fakeModule{meta: meta, handler: func(in *fakeInst, name string, arg []byte) ([]byte, error) {
		var ev struct {
			Type string          `json:"type"`
			Data json.RawMessage `json:"data"`
		}
		_ = json.Unmarshal(arg, &ev)
		mu.Lock()
		got = append(got, ev.Type+":"+string(ev.Data))
		mu.Unlock()
		if ev.Type == EventRedemption {
			in.hc("timer.set", map[string]any{"id": "t", "ms": minTimerMS})
		}
		return nil, nil
	}}
	p := newTestPlugin(t, mod, Services{})
	if p.Deliver(EventRedemption, map[string]string{"id": "1"}) {
		t.Fatal("without the grant the redemption must not be delivered")
	}
	p.setGrants([]string{PermRedeem, PermTimer})
	if !p.Deliver(EventRedemption, map[string]string{"id": "2"}) {
		t.Fatal("granted and subscribed: must be delivered")
	}
	waitFor(t, "redemption and timer events", func() bool { mu.Lock(); defer mu.Unlock(); return len(got) >= 2 })
	mu.Lock()
	defer mu.Unlock()
	if got[0] != `twitch.redemption:{"id":"2"}` || got[1] != `timer:{"id":"t"}` {
		t.Fatalf("events: %v", got)
	}
	// sad path: чужое событие, на которое подписки нет
	other := baseMeta("other")
	omod := &fakeModule{meta: other, handler: func(*fakeInst, string, []byte) ([]byte, error) { return nil, nil }}
	op := newTestPlugin(t, omod, Services{})
	if op.Deliver("something.else", 1) {
		t.Fatal("an unknown event must not be delivered")
	}
}

func TestTimerRulesAndReplace(t *testing.T) {
	meta := baseMeta("tm")
	meta.Permissions = []string{PermTimer}
	mod := &fakeModule{meta: meta, handler: func(*fakeInst, string, []byte) ([]byte, error) { return nil, nil }}
	p := newTestPlugin(t, mod, Services{}, PermTimer)
	call := func(arg any) map[string]any {
		b, _ := json.Marshal(arg)
		var m map[string]any
		_ = json.Unmarshal(p.hostCall(context.Background(), "timer.set", b), &m)
		return m
	}
	if r := call(map[string]any{"id": "a", "ms": 1}); r["error"] == nil {
		t.Fatal("too short timer must be rejected")
	}
	if r := call(map[string]any{"id": "", "ms": 100}); r["error"] == nil {
		t.Fatal("empty id must be rejected")
	}
	for i := 0; i < maxTimers; i++ {
		if r := call(map[string]any{"id": "t" + string(rune('a'+i)), "ms": 100000}); r["error"] != nil {
			t.Fatalf("timer %d: %v", i, r)
		}
	}
	if r := call(map[string]any{"id": "extra", "ms": 100000}); r["error"] == nil {
		t.Fatal("the timer limit must be enforced")
	}
	if r := call(map[string]any{"id": "ta", "ms": 200000}); r["error"] != nil {
		t.Fatal("replacing a timer by id must not count against the limit")
	}
	p.hostCall(context.Background(), "timer.clear", []byte(`{"id":"ta"}`))
	if r := call(map[string]any{"id": "extra", "ms": 100000}); r["error"] != nil {
		t.Fatalf("clear must free a slot: %v", r)
	}
}

func TestFetchEventAndLocalNetworkBlock(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("pong " + r.Method))
	}))
	defer srv.Close()

	run := func(client *http.Client) fetchResult {
		meta := baseMeta("fetcher")
		meta.Permissions = []string{PermNetHTTP}
		meta.Subscriptions = nil
		meta.Routes = []Route{{"GET", "/go", "go"}}
		resCh := make(chan fetchResult, 1)
		mod := &fakeModule{meta: meta, handler: func(in *fakeInst, name string, arg []byte) ([]byte, error) {
			if name == "go" {
				if r := in.hc("http.fetch", map[string]any{"id": "r1", "url": srv.URL}); r["error"] != nil {
					t.Errorf("fetch rejected: %v", r)
				}
				return nil, nil
			}
			var ev struct {
				Type string      `json:"type"`
				Data fetchResult `json:"data"`
			}
			_ = json.Unmarshal(arg, &ev)
			if ev.Type == EventHTTPResponse {
				resCh <- ev.Data
			}
			return nil, nil
		}}
		p := newTestPlugin(t, mod, Services{Fetch: client}, PermNetHTTP)
		if _, err := p.Handle(context.Background(), Request{Method: "GET", Path: "/go"}); err != nil {
			t.Fatal(err)
		}
		return <-resCh
	}
	if r := run(newFetchClient(true)); r.Status != 200 || r.Body != "pong GET" || r.ID != "r1" {
		t.Fatalf("local fetch (allowed in this test): %+v", r)
	}
	// sad path: продовый клиент не пускает в loopback, и это приходит событием с error
	if r := run(newFetchClient(false)); r.Error == "" || r.Status != 0 {
		t.Fatalf("loopback must be blocked by the dialer: %+v", r)
	}
}

func TestFetchValidateAndBlockedAddr(t *testing.T) {
	for _, a := range []fetchArgs{
		{ID: "", URL: "https://example.com"},
		{ID: "x", URL: "file:///etc/passwd"},
		{ID: "x", URL: "ftp://example.com"},
		{ID: "x", URL: "http://"},
		{ID: "x", Method: "TRACE", URL: "https://example.com"},
		{ID: "x", URL: "https://example.com", Body: strings.Repeat("a", fetchMaxReq+1)},
	} {
		if a.validate() == nil {
			t.Errorf("must be rejected: %+v", a)
		}
	}
	ok := fetchArgs{ID: "x", Method: "post", URL: "https://example.com"}
	if err := ok.validate(); err != nil || ok.Method != "POST" {
		t.Fatalf("valid args: %v %+v", err, ok)
	}
	for ip, blocked := range map[string]bool{
		"127.0.0.1": true, "::1": true, "10.1.2.3": true, "192.168.0.5": true, "172.16.0.1": true,
		"169.254.169.254": true, "100.64.0.1": true, "0.0.0.0": true, "224.0.0.1": true, "::ffff:127.0.0.1": true,
		"fe80::1": true, "fc00::1": true,
		"8.8.8.8": false, "1.1.1.1": false, "2606:4700:4700::1111": false,
	} {
		err := publicOnly("tcp", ip+":80", nil)
		if ip == "::1" || strings.Contains(ip, ":") && !strings.HasPrefix(ip, "::ffff") {
			err = publicOnly("tcp", "["+ip+"]:80", nil)
		}
		if (err != nil) != blocked {
			t.Errorf("%s: blocked=%v, err=%v", ip, blocked, err)
		}
	}
}

func TestFetchInflightLimit(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-block }))
	defer srv.Close()
	defer close(block)
	meta := baseMeta("limit")
	meta.Permissions = []string{PermNetHTTP}
	mod := &fakeModule{meta: meta, handler: func(*fakeInst, string, []byte) ([]byte, error) { return nil, nil }}
	p := newTestPlugin(t, mod, Services{Fetch: newFetchClient(true)}, PermNetHTTP)
	var last map[string]any
	for i := 0; i <= fetchMaxInfly; i++ {
		b, _ := json.Marshal(map[string]any{"id": "r" + string(rune('a'+i)), "url": srv.URL})
		last = nil
		_ = json.Unmarshal(p.hostCall(context.Background(), "http.fetch", b), &last)
		if i < fetchMaxInfly && last["error"] != nil {
			t.Fatalf("request %d rejected: %v", i, last)
		}
	}
	if last["error"] == nil {
		t.Fatal("request over the in-flight limit must be rejected")
	}
}

func TestRedeemOp(t *testing.T) {
	meta := baseMeta("rd")
	meta.Permissions = []string{PermRedeem}
	meta.Subscriptions = []string{EventRedemption}
	var calls []string
	var mu sync.Mutex
	svc := Services{Redeem: func(_ context.Context, rw, id, st string) error {
		mu.Lock()
		calls = append(calls, rw+"/"+id+"/"+st)
		mu.Unlock()
		if id == "fail" {
			return errors.New("helix: 400")
		}
		return nil
	}}
	mod := &fakeModule{meta: meta, handler: func(*fakeInst, string, []byte) ([]byte, error) { return nil, nil }}
	p := newTestPlugin(t, mod, svc, PermRedeem)
	do := func(a map[string]string) map[string]any {
		b, _ := json.Marshal(a)
		var m map[string]any
		_ = json.Unmarshal(p.hostCall(context.Background(), "twitch.redemption.update", b), &m)
		return m
	}
	if r := do(map[string]string{"rewardId": "rw", "id": "r1", "status": "FULFILLED"}); r["error"] != nil {
		t.Fatal(r)
	}
	// sad path
	if r := do(map[string]string{"rewardId": "rw", "id": "r1", "status": "UNFULFILLED"}); r["error"] == nil {
		t.Fatal("only FULFILLED/CANCELED are allowed")
	}
	if r := do(map[string]string{"rewardId": "", "id": "r1", "status": "FULFILLED"}); r["error"] == nil {
		t.Fatal("rewardId is required")
	}
	if r := do(map[string]string{"rewardId": "rw", "id": "fail", "status": "CANCELED"}); r["error"] != "helix: 400" {
		t.Fatalf("host error must reach the plugin: %v", r)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 2 || calls[0] != "rw/r1/FULFILLED" {
		t.Fatalf("calls: %v", calls)
	}
}

func TestUnknownOpAndLog(t *testing.T) {
	mod := &fakeModule{meta: baseMeta("misc"), handler: func(*fakeInst, string, []byte) ([]byte, error) { return nil, nil }}
	p := newTestPlugin(t, mod, Services{})
	var m map[string]any
	_ = json.Unmarshal(p.hostCall(context.Background(), "rm.rf", nil), &m)
	if m["error"] == nil {
		t.Fatal("unknown op must be an error")
	}
	if out := p.hostCall(context.Background(), "log", []byte(`{"level":"warn","msg":"hi","fields":{"a":1}}`)); out != nil {
		t.Fatalf("log returns nothing, got %s", out)
	}
	lw := &lineWriter{log: quietLog()}
	if n, err := lw.Write([]byte("panic: x\ngoroutine 1")); err != nil || n != 20 {
		t.Fatalf("lineWriter: %d %v", n, err)
	}
}

type fakeBus struct {
	mu        sync.Mutex
	declared  map[string][]string
	forgotten []string
	pub       []string
	err       error
}

func (b *fakeBus) Declare(plugin string, topics []string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.declared == nil {
		b.declared = map[string][]string{}
	}
	b.declared[plugin] = topics
	return nil
}
func (b *fakeBus) Forget(plugin string) {
	b.mu.Lock()
	b.forgotten = append(b.forgotten, plugin)
	b.mu.Unlock()
}
func (b *fakeBus) Publish(plugin, topic string, data json.RawMessage, retain bool) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.err != nil {
		return b.err
	}
	b.pub = append(b.pub, fmt.Sprintf("%s/%s/%s/%v", plugin, topic, data, retain))
	return nil
}

func TestEventsPublishNeedsPermissionAndUsesHostNamespace(t *testing.T) {
	bus := &fakeBus{}
	meta := baseMeta("music")
	meta.Events = []EventDecl{{ID: "now"}}
	meta.Permissions = []string{PermPublish} // выдать можно только то, что плагин запросил
	mod := &fakeModule{meta: meta, handler: func(*fakeInst, string, []byte) ([]byte, error) { return nil, nil }}
	denied := newTestPlugin(t, mod, Services{Events: bus})
	var m map[string]any
	_ = json.Unmarshal(denied.hostCall(context.Background(), "events.publish", []byte(`{"topic":"now","data":1}`)), &m)
	if m["error"] == nil || len(bus.pub) != 0 {
		t.Fatalf("без права публикация должна отклоняться: %v %v", m, bus.pub)
	}
	p := newTestPlugin(t, &fakeModule{meta: meta, handler: mod.handler}, Services{Events: bus}, PermPublish)
	// плагин не может указать чужое пространство имён: plugin в аргументе игнорируется
	out := p.hostCall(context.Background(), "events.publish", []byte(`{"plugin":"evil","topic":"now","data":{"a":1},"retain":true}`))
	if string(out) != "{}" || len(bus.pub) != 1 || bus.pub[0] != `music/now/{"a":1}/true` {
		t.Fatalf("out=%s pub=%v", out, bus.pub)
	}
	bus.err = errors.New("topic is not declared by the plugin")
	m = nil
	_ = json.Unmarshal(p.hostCall(context.Background(), "events.publish", []byte(`{"topic":"other","data":1}`)), &m)
	if m["error"] != "topic is not declared by the plugin" {
		t.Fatalf("ошибка брокера должна дойти до плагина: %v", m)
	}
	if got := bus.declared["music"]; len(got) != 1 || got[0] != "now" {
		t.Fatalf("topics: %v", bus.declared)
	}
}

func TestEventsUnavailableWithoutBusAndForgetOnStop(t *testing.T) {
	a := baseMeta("a")
	a.Permissions = []string{PermPublish}
	mod := &fakeModule{meta: a, handler: func(*fakeInst, string, []byte) ([]byte, error) { return nil, nil }}
	p := newTestPlugin(t, mod, Services{}, PermPublish)
	var m map[string]any
	_ = json.Unmarshal(p.hostCall(context.Background(), "events.publish", []byte(`{"topic":"x","data":1}`)), &m)
	if m["error"] == nil {
		t.Fatal("без брокера публикация недоступна")
	}
	bus := &fakeBus{}
	p2 := newTestPlugin(t, &fakeModule{meta: baseMeta("b"), handler: mod.handler}, Services{Events: bus})
	p2.Stop()
	if len(bus.forgotten) != 1 || bus.forgotten[0] != "b" {
		t.Fatalf("Stop должен снимать топики: %v", bus.forgotten)
	}
}

func TestChatSendNeedsPermissionAndIsLimited(t *testing.T) {
	meta := baseMeta("music")
	meta.Permissions = []string{PermChat}
	mod := &fakeModule{meta: meta, handler: func(*fakeInst, string, []byte) ([]byte, error) { return nil, nil }}
	sent := make(chan string, 10)
	svc := Services{Say: func(_ context.Context, text string) error { sent <- text; return nil }}

	denied := newTestPlugin(t, mod, svc)
	var m map[string]any
	_ = json.Unmarshal(denied.hostCall(context.Background(), "chat.send", []byte(`{"text":"hi"}`)), &m)
	if m["error"] == nil || len(sent) != 0 {
		t.Fatalf("без права сообщение не уходит: %v", m)
	}

	p := newTestPlugin(t, &fakeModule{meta: meta, handler: mod.handler}, svc, PermChat)
	if out := p.hostCall(context.Background(), "chat.send", []byte(`{"text":"  привет  ","channel":"evil","as":"streamer"}`)); string(out) != "{}" {
		t.Fatalf("out=%s", out)
	}
	select {
	case got := <-sent:
		if got != "привет" { // канал и аккаунт плагин выбрать не может: хост их не читает
			t.Fatalf("got %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("сообщение не отправлено")
	}
	// плохой текст
	for _, a := range []string{`{"text":""}`, `{"text":"   "}`, `{"text":"` + strings.Repeat("я", chatMaxText+1) + `"}`, `{"text":1}`, `nope`} {
		m = nil
		_ = json.Unmarshal(p.hostCall(context.Background(), "chat.send", []byte(a)), &m)
		if m["error"] == nil {
			t.Fatalf("%.30s должно отклоняться", a)
		}
	}
	// всплеск: chatBurst сообщений проходят (одно уже ушло), следующее отклоняется
	ok, limited := 1, 0
	for i := 0; i < chatBurst+2; i++ {
		m = nil
		_ = json.Unmarshal(p.hostCall(context.Background(), "chat.send", []byte(`{"text":"x"}`)), &m)
		if m["error"] == nil {
			ok++
		} else if m["error"] == "rate limit exceeded" {
			limited++
		}
	}
	if ok != chatBurst || limited != 3 {
		t.Fatalf("ok=%d limited=%d", ok, limited)
	}
}

func TestChatSendUnavailableWithoutService(t *testing.T) {
	meta := baseMeta("a")
	meta.Permissions = []string{PermChat}
	mod := &fakeModule{meta: meta, handler: func(*fakeInst, string, []byte) ([]byte, error) { return nil, nil }}
	p := newTestPlugin(t, mod, Services{}, PermChat)
	var m map[string]any
	_ = json.Unmarshal(p.hostCall(context.Background(), "chat.send", []byte(`{"text":"hi"}`)), &m)
	if m["error"] == nil {
		t.Fatal("без сервиса чата операция недоступна")
	}
}
