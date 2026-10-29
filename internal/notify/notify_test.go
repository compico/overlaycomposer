package notify

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestUpsertKeepsReadAndDedups(t *testing.T) {
	c := Open("")
	c.Upsert(Item{Key: "a", Kind: KindPluginUpdate, Title: "v1"})
	c.MarkRead("a")
	c.Upsert(Item{Key: "a", Kind: KindPluginUpdate, Title: "v1 updated"})
	l := c.List()
	if len(l) != 1 || !l[0].Read || l[0].Title != "v1 updated" || c.Unread() != 0 {
		t.Fatalf("%+v", l)
	}
	c.Upsert(Item{Key: "b", Kind: KindAppUpdate, Title: "app"})
	if c.Unread() != 1 {
		t.Fatal("unread")
	}
	c.MarkRead("")
	if c.Unread() != 0 {
		t.Fatal("mark all")
	}
	c.Upsert(Item{Key: "plugin:x@2", Kind: KindPluginUpdate})
	c.DismissPrefix("plugin:x@")
	if len(c.List()) != 2 {
		t.Fatal("dismiss prefix")
	}
	c.Dismiss("a")
	if len(c.List()) != 1 {
		t.Fatal("dismiss one")
	}
	c.Dismiss("")
	if len(c.List()) != 0 {
		t.Fatal("dismiss all")
	}
}

func TestPersistsAcrossOpen(t *testing.T) {
	p := filepath.Join(t.TempDir(), "n.json")
	c := Open(p)
	c.Upsert(Item{Key: "a", Kind: KindAppUpdate, Title: "x"})
	c.MarkRead("a")
	l := Open(p).List()
	if len(l) != 1 || !l[0].Read {
		t.Fatalf("%+v", l)
	}
	if Open(filepath.Join(t.TempDir(), "missing.json")).Unread() != 0 {
		t.Fatal("missing file")
	}
}

func TestCapDropsOldest(t *testing.T) {
	c := Open("")
	for i := 0; i < maxItems+5; i++ {
		c.Upsert(Item{Key: string(rune('A' + i)), Kind: KindAppUpdate, At: int64(i + 1)})
	}
	l := c.List()
	if len(l) != maxItems || l[len(l)-1].At != 6 {
		t.Fatalf("len=%d last=%d", len(l), l[len(l)-1].At)
	}
}

func finder(n *int, list ...Candidate) Finder {
	return func(context.Context) ([]Candidate, bool) { *n++; return list, true }
}

func TestCheckAddsAndResolves(t *testing.T) {
	c := Open("")
	var n int
	if err := c.Check(context.Background(), false, finder(&n, Candidate{Key: "p@2", Title: "P 2", Data: map[string]string{"to": "2"}})); err != nil {
		t.Fatal(err)
	}
	if l := c.List(); len(l) != 1 || l[0].Kind != KindPluginUpdate || string(l[0].Data) != `{"to":"2"}` {
		t.Fatalf("%+v", l)
	}
	c.Upsert(Item{Key: "app", Kind: KindAppUpdate}) // чужой вид не трогаем
	// плагин обновили: кандидатов нет → напоминание снимается
	c.now = func() time.Time { return time.Now().Add(CheckInterval + time.Minute) }
	if err := c.Check(context.Background(), false, finder(&n)); err != nil {
		t.Fatal(err)
	}
	if l := c.List(); len(l) != 1 || l[0].Key != "app" {
		t.Fatalf("%+v", l)
	}
}

func TestCheckThrottleSurvivesRestart(t *testing.T) {
	p := filepath.Join(t.TempDir(), "n.json")
	var n int
	for i := 0; i < 15; i++ { // crashloop: каждый «запуск» открывает центр заново
		Open(p).Check(context.Background(), false, finder(&n))
	}
	if n != 1 {
		t.Fatalf("проверок: %d, ожидалась 1", n)
	}
	c := Open(p)
	if err := c.Check(context.Background(), true, finder(&n)); err != ErrTooSoon {
		t.Fatalf("кнопка сразу после проверки: %v", err)
	}
	c.now = func() time.Time { return time.Now().Add(ManualInterval + time.Second) }
	if err := c.Check(context.Background(), true, finder(&n)); err != nil || n != 2 {
		t.Fatalf("кнопка через минуту: %v n=%d", err, n)
	}
}

func TestFailedCheckKeepsItemsAndRetriesSooner(t *testing.T) {
	p := filepath.Join(t.TempDir(), "n.json")
	c := Open(p)
	c.Upsert(Item{Key: "p@2", Kind: KindPluginUpdate})
	fail := func(context.Context) ([]Candidate, bool) { return nil, false }
	if err := c.Check(context.Background(), false, fail); err == nil {
		t.Fatal("ожидалась ошибка")
	}
	if len(c.List()) != 1 {
		t.Fatal("при недоступной сети уведомления снимать нельзя")
	}
	if err := Open(p).Check(context.Background(), false, fail); err != ErrTooSoon {
		t.Fatalf("сразу после неудачи: %v", err)
	}
	c2 := Open(p)
	c2.now = func() time.Time { return time.Now().Add(FailedInterval + time.Minute) }
	var n int
	if err := c2.Check(context.Background(), false, finder(&n)); err != nil || n != 1 {
		t.Fatalf("через FailedInterval: %v", err)
	}
}

func TestSyncAnnouncesOnceAndResolves(t *testing.T) {
	c := Open("")
	a := Item{Key: "twitch:bot:token", Title: "Reconnect"}
	c.Sync(KindTwitchReconnect, []Item{a})
	if l := c.List(); len(l) != 1 || l[0].Kind != KindTwitchReconnect || c.Unread() != 1 {
		t.Fatalf("first sync: %+v", l)
	}
	// повторная синхронизация не плодит дубли и не сбрасывает «прочитано»
	c.MarkRead(a.Key)
	c.Sync(KindTwitchReconnect, []Item{a})
	if len(c.List()) != 1 || c.Unread() != 0 {
		t.Fatalf("second sync: %+v", c.List())
	}
	// пользователь убрал — пока условие держится, уведомление не возвращается
	c.Dismiss(a.Key)
	c.Sync(KindTwitchReconnect, []Item{a})
	if len(c.List()) != 0 {
		t.Fatalf("dismissed must stay dismissed: %+v", c.List())
	}
	// условие пропало (аккаунт переподключён) и появилось снова — это новое уведомление
	c.Sync(KindTwitchReconnect, nil)
	c.Sync(KindTwitchReconnect, []Item{a})
	if len(c.List()) != 1 {
		t.Fatalf("recurrence must notify again: %+v", c.List())
	}
	// пропавшее условие снимает уведомление, чужие виды не трогает
	c.Upsert(Item{Key: "app:1", Kind: KindAppUpdate})
	c.Sync(KindTwitchReconnect, nil)
	if l := c.List(); len(l) != 1 || l[0].Kind != KindAppUpdate {
		t.Fatalf("resolve: %+v", l)
	}
}
