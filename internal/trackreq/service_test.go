package trackreq

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/compico/overlaycomposer/internal/twitch"
)

type memStore struct {
	mu   sync.Mutex
	reqs []Request
	cfg  Config
}

func (m *memStore) Add(r Request) (Request, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, x := range m.reqs {
		if x.RedemptionID == r.RedemptionID {
			return x, false, nil
		}
	}
	r.ID = int64(len(m.reqs) + 1)
	r.CreatedAt = time.Now()
	m.reqs = append(m.reqs, r)
	return r, true, nil
}
func (m *memStore) Get(id int64) (*Request, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.reqs {
		if m.reqs[i].ID == id {
			c := m.reqs[i]
			return &c, nil
		}
	}
	return nil, nil
}
func (m *memStore) List(limit int) ([]Request, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Request
	for i := len(m.reqs) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, m.reqs[i])
	}
	return out, nil
}
func (m *memStore) first(status string) *Request {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.reqs {
		if m.reqs[i].Status == status {
			c := m.reqs[i]
			return &c
		}
	}
	return nil
}
func (m *memStore) NextQueued() (*Request, error) { return m.first(StatusQueued), nil }
func (m *memStore) Active() (*Request, error)     { return m.first(StatusSent), nil }
func (m *memStore) SetStatus(id int64, status, e string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.reqs {
		if m.reqs[i].ID == id {
			m.reqs[i].Status, m.reqs[i].Error = status, e
		}
	}
	return nil
}
func (m *memStore) LoadConfig() (Config, error) { return m.cfg, nil }
func (m *memStore) SaveConfig(c Config) error   { m.cfg = c; return nil }
func (m *memStore) status(id int64) string      { r, _ := m.Get(id); return r.Status }

type fakeTwitch struct {
	mu    sync.Mutex
	calls []string
	err   error
}

func (f *fakeTwitch) UpdateRedemption(_ context.Context, reward, red, status string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, red+":"+status)
	return f.err
}
func (f *fakeTwitch) list() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

type fakePlayer struct {
	mu   sync.Mutex
	on   bool
	cmds []Command
	err  error
}

func (p *fakePlayer) Connected() bool { p.mu.Lock(); defer p.mu.Unlock(); return p.on }
func (p *fakePlayer) Send(c Command) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return p.err
	}
	p.cmds = append(p.cmds, c)
	return nil
}
func (p *fakePlayer) set(on bool) { p.mu.Lock(); p.on = on; p.mu.Unlock() }
func (p *fakePlayer) sent() []Command {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]Command(nil), p.cmds...)
}

func newSvc(t *testing.T) (*Service, *memStore, *fakeTwitch, *fakePlayer) {
	t.Helper()
	st := &memStore{cfg: Config{Enabled: true, RewardID: "rw"}}
	tw, pl := &fakeTwitch{}, &fakePlayer{on: true}
	s, err := New(st, tw, pl, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)
	return s, st, tw, pl
}

func red(id, input string) twitch.Redemption {
	return twitch.Redemption{ID: id, RewardID: "rw", UserLogin: "v", UserName: "V", Input: input}
}
func link(n int) string { return fmt.Sprintf("https://music.yandex.ru/album/%d/track/%d", n, n*10) }

func waitTw(t *testing.T, tw *fakeTwitch, n int) []string {
	t.Helper()
	for i := 0; i < 200; i++ {
		if l := tw.list(); len(l) >= n {
			return l
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("ждали %d вызовов Twitch, было %v", n, tw.list())
	return nil
}

func TestFIFOOneAtATimeAndConfirmedStart(t *testing.T) {
	s, st, tw, pl := newSvc(t)
	s.Handle(red("r1", link(1)))
	s.Handle(red("r2", link(2)))
	s.Handle(red("r3", link(3)))
	if c := pl.sent(); len(c) != 1 || c[0].ID != 1 || c[0].TrackID != "10" || c[0].AlbumID != "1" || c[0].Resend {
		t.Fatalf("плееру должна уйти только первая заявка: %+v", c)
	}
	if st.status(1) != StatusSent || st.status(2) != StatusQueued {
		t.Fatal("статусы после отправки")
	}
	// отправка — не подтверждение: пока старт не подтверждён, вторая не уходит
	if err := s.PlayerStarted(1); err != nil {
		t.Fatal(err)
	}
	if c := pl.sent(); len(c) != 2 || c[1].ID != 2 {
		t.Fatalf("после старта первой уходит вторая: %+v", c)
	}
	if got := waitTw(t, tw, 1); got[0] != "r1:FULFILLED" {
		t.Fatalf("%v", got)
	}
	_ = s.PlayerStarted(2)
	_ = s.PlayerStarted(3)
	for i := int64(1); i <= 3; i++ {
		if st.status(i) != StatusPlayed {
			t.Fatalf("заявка %d: %s", i, st.status(i))
		}
	}
}

func TestDuplicateAndForeignAndDisabled(t *testing.T) {
	s, st, _, pl := newSvc(t)
	s.Handle(red("r1", link(1)))
	s.Handle(red("r1", link(1))) // повторная доставка
	other := red("r2", link(2))
	other.RewardID = "other"
	s.Handle(other) // чужая награда
	if len(st.reqs) != 1 || len(pl.sent()) != 1 {
		t.Fatalf("заявок %d, команд %d", len(st.reqs), len(pl.sent()))
	}
	if err := s.SetConfig(Config{Enabled: false, RewardID: "rw"}); err != nil {
		t.Fatal(err)
	}
	s.Handle(red("r3", link(3)))
	if len(st.reqs) != 1 {
		t.Fatal("при выключенной функции заявки не принимаются")
	}
}

func TestBadLinkRejectedAndRefunded(t *testing.T) {
	s, st, tw, pl := newSvc(t)
	s.Handle(red("r1", "https://evil.example/album/1/track/2"))
	if got := waitTw(t, tw, 1); got[0] != "r1:CANCELED" {
		t.Fatalf("баллы должны вернуться: %v", got)
	}
	if st.status(1) != StatusRejected || st.reqs[0].Error == "" || len(pl.sent()) != 0 {
		t.Fatalf("%+v cmds=%v", st.reqs[0], pl.sent())
	}
	// повтор отклонённого погашения не создаёт ничего и не возвращает баллы второй раз
	s.Handle(red("r1", "https://evil.example/album/1/track/2"))
	time.Sleep(50 * time.Millisecond)
	if len(tw.list()) != 1 || len(st.reqs) != 1 {
		t.Fatalf("%v %d", tw.list(), len(st.reqs))
	}
}

func TestPlayerOfflineThenConnected(t *testing.T) {
	s, st, _, pl := newSvc(t)
	pl.set(false)
	s.Handle(red("r1", link(1)))
	s.Handle(red("r2", link(2)))
	if len(pl.sent()) != 0 || st.status(1) != StatusQueued {
		t.Fatal("без плеера заявки ждут")
	}
	pl.set(true)
	s.PlayerConnected()
	if c := pl.sent(); len(c) != 1 || c[0].ID != 1 {
		t.Fatalf("%+v", c)
	}
}

func TestRestartResendsActiveWithFlag(t *testing.T) {
	s, st, _, pl := newSvc(t)
	s.Handle(red("r1", link(1)))
	s.Handle(red("r2", link(2)))
	// «перезапуск»: новый сервис над той же базой, плеер подключается заново
	pl2 := &fakePlayer{on: true}
	s2, _ := New(st, &fakeTwitch{}, pl2, nil)
	defer s2.Stop()
	s2.PlayerConnected()
	c := pl2.sent()
	if len(c) != 1 || c[0].ID != 1 || !c[0].Resend {
		t.Fatalf("активная заявка уходит повторно с Resend, вторая ждёт: %+v", c)
	}
	_ = pl
}

func TestPlayerFailedCancelsAndMovesOn(t *testing.T) {
	s, st, tw, pl := newSvc(t)
	s.Handle(red("r1", link(1)))
	s.Handle(red("r2", link(2)))
	if err := s.PlayerFailed(1, "player not ready"); err != nil {
		t.Fatal(err)
	}
	if st.status(1) != StatusFailed || st.reqs[0].Error != "player not ready" {
		t.Fatalf("%+v", st.reqs[0])
	}
	if got := waitTw(t, tw, 1); got[0] != "r1:CANCELED" {
		t.Fatalf("%v", got)
	}
	if c := pl.sent(); len(c) != 2 || c[1].ID != 2 {
		t.Fatalf("очередь не должна вставать: %+v", c)
	}
}

func TestFinishSadPaths(t *testing.T) {
	s, _, _, pl := newSvc(t)
	if err := s.PlayerStarted(99); !errors.Is(err, ErrUnknownRequest) {
		t.Fatalf("неизвестная заявка: %v", err)
	}
	pl.set(false)
	s.Handle(red("r1", link(1))) // queued, плееру не отдана
	if err := s.PlayerStarted(1); err == nil || !strings.Contains(err.Error(), "not sent") {
		t.Fatalf("«старт» неотданной заявки принимать нельзя: %v", err)
	}
	pl.set(true)
	s.PlayerConnected()
	if err := s.PlayerStarted(1); err != nil {
		t.Fatal(err)
	}
	if err := s.PlayerStarted(1); err == nil {
		t.Fatal("повторное подтверждение должно отклоняться")
	}
	if err := s.Cancel(1); err == nil {
		t.Fatal("сыгранную заявку отменить нельзя")
	}
}

func TestPlayerRefusesCommand(t *testing.T) {
	s, st, _, pl := newSvc(t)
	pl.err = errors.New("socket closed")
	s.Handle(red("r1", link(1)))
	if st.status(1) != StatusQueued {
		t.Fatalf("при отказе плеера заявка остаётся в очереди, а не теряется: %s", st.status(1))
	}
}

func TestQueueLimit(t *testing.T) {
	s, st, tw, pl := newSvc(t)
	pl.set(false)
	for i := 1; i <= maxQueued+1; i++ {
		s.Handle(red(fmt.Sprintf("r%d", i), link(i)))
	}
	last := st.reqs[len(st.reqs)-1]
	if last.Status != StatusRejected || last.Error != "the queue is full" {
		t.Fatalf("%+v", last)
	}
	waitTw(t, tw, 1)
}

func TestCancelAndTwitchErrorIsNotFatal(t *testing.T) {
	s, st, tw, _ := newSvc(t)
	tw.err = errors.New("403 not the owner of this reward")
	s.Handle(red("r1", link(1)))
	if err := s.Cancel(1); err != nil {
		t.Fatal(err)
	}
	waitTw(t, tw, 1)
	if st.status(1) != StatusCanceled {
		t.Fatal("ошибка Twitch не должна мешать снять заявку")
	}
}

type fakeES struct{ run func(ctx context.Context) }

func (f fakeES) Run(ctx context.Context) { f.run(ctx) }

func TestConfigStartsAndStopsEventSource(t *testing.T) {
	st := &memStore{}
	started := make(chan struct{}, 4)
	stopped := make(chan struct{}, 4)
	var onRedeem func(twitch.Redemption)
	var onState func(string, string)
	s, _ := New(st, &fakeTwitch{}, &fakePlayer{on: true}, func(r func(twitch.Redemption), st func(string, string)) EventSource {
		onRedeem, onState = r, st
		return fakeES{func(ctx context.Context) { started <- struct{}{}; <-ctx.Done(); stopped <- struct{}{} }}
	})
	ctx := context.Background()
	s.Start(ctx)
	select {
	case <-started:
		t.Fatal("выключенная функция не должна открывать соединение с Twitch")
	case <-time.After(50 * time.Millisecond):
	}
	if err := s.SetConfig(Config{Enabled: true, RewardID: "rw"}); err != nil {
		t.Fatal(err)
	}
	<-started
	onState(twitch.ESConnected, "")
	if got := s.Status(); got.Twitch != twitch.ESConnected || !got.PlayerConnected || !got.Config.Enabled {
		t.Fatalf("%+v", got)
	}
	onRedeem(red("r1", link(1)))
	if len(st.reqs) != 1 {
		t.Fatal("погашение из источника должно стать заявкой")
	}
	if err := s.SetConfig(Config{Enabled: false}); err != nil {
		t.Fatal(err)
	}
	<-stopped
	if s.Status().Twitch != twitch.ESDisconnected {
		t.Fatalf("%+v", s.Status())
	}
	s.Stop()
}
