package events

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newB(t *testing.T, topics ...string) (*Broker, *clock) {
	t.Helper()
	c := &clock{t: time.Unix(1000, 0)}
	b := New(Options{Now: c.now})
	if err := b.Declare("music", topics); err != nil {
		t.Fatal(err)
	}
	return b, c
}

func recv(t *testing.T, s *Sub) Event {
	t.Helper()
	select {
	case ev, ok := <-s.C:
		if !ok {
			t.Fatal("канал закрыт")
		}
		return ev
	case <-time.After(time.Second):
		t.Fatal("событие не пришло")
	}
	return Event{}
}

func none(t *testing.T, s *Sub) {
	t.Helper()
	select {
	case ev := <-s.C:
		t.Fatalf("лишнее событие: %+v", ev)
	default:
	}
}

func TestPublishDeliversToSubscribersOfTheTopicOnly(t *testing.T) {
	b, _ := newB(t, "now", "queue")
	now, _ := b.Subscribe("music", "now", 0)
	queue, _ := b.Subscribe("music", "queue", 0)
	if err := b.Publish("music", "now", json.RawMessage(`{"a":1}`), false); err != nil {
		t.Fatal(err)
	}
	ev := recv(t, now)
	if ev.Plugin != "music" || ev.Topic != "now" || string(ev.Data) != `{"a":1}` || ev.Seq != 1 || ev.Retain {
		t.Fatalf("event: %+v", ev)
	}
	none(t, queue)
}

func TestTopicNamespaceIsPerPlugin(t *testing.T) {
	b, _ := newB(t, "now")
	if err := b.Declare("other", []string{"now"}); err != nil {
		t.Fatal(err)
	}
	mine, _ := b.Subscribe("music", "now", 0)
	theirs, _ := b.Subscribe("other", "now", 0)
	_ = b.Publish("other", "now", json.RawMessage(`1`), false)
	recv(t, theirs)
	none(t, mine)
	// чужой плагин не объявлял топик — публиковать и подписываться нельзя
	if err := b.Publish("ghost", "now", json.RawMessage(`1`), false); err != ErrNotDeclared {
		t.Fatalf("publish: %v", err)
	}
	if _, err := b.Subscribe("ghost", "now", 0); err != ErrNotDeclared {
		t.Fatalf("subscribe: %v", err)
	}
}

func TestUndeclaredAndBadTopicsAreRejected(t *testing.T) {
	b, _ := newB(t, "now")
	if err := b.Publish("music", "secret", json.RawMessage(`1`), false); err != ErrNotDeclared {
		t.Fatalf("unknown: %v", err)
	}
	for _, bad := range []string{"", "Now", "a b", "a/b", strings.Repeat("a", 65), "ключ", "../x"} {
		if err := b.Publish("music", bad, json.RawMessage(`1`), false); err != ErrBadTopic {
			t.Errorf("publish %q: %v", bad, err)
		}
		if _, err := b.Subscribe("music", bad, 0); err != ErrBadTopic {
			t.Errorf("subscribe %q: %v", bad, err)
		}
	}
}

func TestDataLimits(t *testing.T) {
	b, _ := newB(t, "now")
	big := json.RawMessage(`"` + strings.Repeat("x", MaxData) + `"`)
	if err := b.Publish("music", "now", big, false); err != ErrTooLarge {
		t.Fatalf("large: %v", err)
	}
	for _, bad := range []string{``, `{`, `nope`, `{"a":}`} {
		if err := b.Publish("music", "now", json.RawMessage(bad), false); err != ErrBadData {
			t.Errorf("%q: %v", bad, err)
		}
	}
	ok := json.RawMessage(`"` + strings.Repeat("x", MaxData-2) + `"`)
	if err := b.Publish("music", "now", ok, false); err != nil {
		t.Fatalf("ровно в предел: %v", err)
	}
}

func TestRetainedValueGoesToNewSubscriberFirst(t *testing.T) {
	b, _ := newB(t, "now")
	_ = b.Publish("music", "now", json.RawMessage(`{"n":1}`), true)
	_ = b.Publish("music", "now", json.RawMessage(`{"n":2}`), true)
	s, _ := b.Subscribe("music", "now", 0)
	ev := recv(t, s)
	if string(ev.Data) != `{"n":2}` || !ev.Retain {
		t.Fatalf("retained: %+v", ev)
	}
	none(t, s)
	// свежее событие без retain сбрасывает сохранённое значение: новый подписчик не получит устаревшее
	_ = b.Publish("music", "now", json.RawMessage(`{"n":3}`), false)
	late, _ := b.Subscribe("music", "now", 0)
	none(t, late)
}

func TestNonRetainedEventsAreNotReplayed(t *testing.T) {
	b, _ := newB(t, "commands")
	_ = b.Publish("music", "commands", json.RawMessage(`1`), false)
	s, _ := b.Subscribe("music", "commands", 0)
	none(t, s)
}

func TestRateLimitPerPlugin(t *testing.T) {
	b, c := newB(t, "now")
	if err := b.Declare("other", []string{"now"}); err != nil {
		t.Fatal(err)
	}
	ok, limited := 0, 0
	for i := 0; i < 100; i++ {
		switch err := b.Publish("music", "now", json.RawMessage(`1`), false); err {
		case nil:
			ok++
		case ErrRate:
			limited++
		default:
			t.Fatal(err)
		}
	}
	if ok != 40 || limited != 60 {
		t.Fatalf("всплеск: ok=%d limited=%d", ok, limited)
	}
	// другой плагин не страдает из-за соседа
	if err := b.Publish("other", "now", json.RawMessage(`1`), false); err != nil {
		t.Fatalf("other: %v", err)
	}
	// ведро пополняется со временем: за секунду 20 событий
	c.t = c.t.Add(time.Second)
	ok = 0
	for i := 0; i < 50; i++ {
		if b.Publish("music", "now", json.RawMessage(`1`), false) == nil {
			ok++
		}
	}
	if ok != 20 {
		t.Fatalf("после секунды: %d", ok)
	}
}

func TestSlowSubscriberLosesOldestAndNeverBlocksPublisher(t *testing.T) {
	c := &clock{t: time.Unix(1000, 0)}
	b := New(Options{Now: c.now, RatePerSec: 1000, Burst: 1000})
	_ = b.Declare("music", []string{"now"})
	slow, _ := b.Subscribe("music", "now", 4)
	done := make(chan struct{})
	go func() {
		for i := 1; i <= 10; i++ {
			_ = b.Publish("music", "now", json.RawMessage(`1`), false)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("издатель завис на медленном подписчике")
	}
	if got := slow.Dropped(); got != 6 {
		t.Fatalf("dropped=%d", got)
	}
	var seqs []uint64
	for i := 0; i < 4; i++ {
		seqs = append(seqs, recv(t, slow).Seq)
	}
	if seqs[0] != 7 || seqs[3] != 10 {
		t.Fatalf("остались самые новые: %v", seqs)
	}
}

func TestCloseForgetAndRedeclare(t *testing.T) {
	b, _ := newB(t, "now", "queue")
	a, _ := b.Subscribe("music", "now", 0)
	q, _ := b.Subscribe("music", "queue", 0)
	_ = b.Publish("music", "now", json.RawMessage(`1`), true)
	a.Close()
	a.Close() // повтор безопасен
	if _, ok := <-a.C; ok {
		// канал содержал retained-событие: прочитали его, дальше должен быть закрыт
		if _, ok := <-a.C; ok {
			t.Fatal("канал не закрыт")
		}
	}
	// после Close события не приходят и не паникуют
	_ = b.Publish("music", "now", json.RawMessage(`2`), false)

	// снятие топика закрывает подписки на него и стирает retained
	if err := b.Declare("music", []string{"queue"}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Subscribe("music", "now", 0); err != ErrNotDeclared {
		t.Fatalf("now снят: %v", err)
	}
	if err := b.Publish("music", "queue", json.RawMessage(`1`), false); err != nil {
		t.Fatal(err)
	}
	recv(t, q)

	b.Forget("music")
	if _, ok := <-q.C; ok {
		t.Fatal("после Forget подписка должна закрыться")
	}
	if err := b.Publish("music", "queue", json.RawMessage(`1`), false); err != ErrNotDeclared {
		t.Fatalf("после Forget: %v", err)
	}
}

func TestValidateDeclared(t *testing.T) {
	if err := ValidateDeclared([]string{"a", "b.c", "d-e_f"}); err != nil {
		t.Fatal(err)
	}
	if ValidateDeclared([]string{"a", "a"}) == nil {
		t.Error("дубликат")
	}
	if ValidateDeclared([]string{"BAD"}) == nil {
		t.Error("плохое имя")
	}
	many := make([]string, MaxTopics+1)
	for i := range many {
		many[i] = "t" + strings.Repeat("a", i%5) + string(rune('a'+i%26)) + string(rune('a'+i/26))
	}
	if ValidateDeclared(many) == nil {
		t.Error("слишком много")
	}
}

func TestConcurrentUse(t *testing.T) {
	c := &clock{t: time.Unix(1000, 0)}
	b := New(Options{Now: c.now, RatePerSec: 1e6, Burst: 1 << 20})
	_ = b.Declare("music", []string{"now"})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				s, err := b.Subscribe("music", "now", 8)
				if err != nil {
					t.Error(err)
					return
				}
				s.Close()
			}
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 500; j++ {
				_ = b.Publish("music", "now", json.RawMessage(`1`), j%2 == 0)
			}
		}()
	}
	wg.Wait()
}
