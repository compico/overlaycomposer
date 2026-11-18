package server

import (
	"encoding/json"
	"testing"
	"time"
)

func TestDeckHubPushesOnlyChanges(t *testing.T) {
	st := deckStateData{Rev: 1, Online: true, Scene: "A", On: []string{"a"}}
	cur := func() deckStateData { return st }
	h := newDeckHub(cur)
	ch, cancel := h.subscribe()

	read := func() deckStateData {
		t.Helper()
		select {
		case b := <-ch:
			var d deckStateData
			if err := json.Unmarshal(b, &d); err != nil || d.T != "state" {
				t.Fatalf("кадр %s: %v", b, err)
			}
			return d
		case <-time.After(2 * time.Second):
			t.Fatal("кадр не пришёл")
		}
		return deckStateData{}
	}
	if d := read(); d.Scene != "A" || len(d.On) != 1 {
		t.Fatalf("начальное состояние: %+v", d)
	}
	h.check() // без изменений — тишина
	select {
	case b := <-ch:
		t.Fatalf("лишний кадр: %s", b)
	default:
	}
	st = deckStateData{Rev: 2, Online: true, Scene: "B", On: []string{"b"}}
	h.check()
	if d := read(); d.Rev != 2 || d.Scene != "B" {
		t.Fatalf("после изменения: %+v", d)
	}
	cancel()
	cancel() // повторная отмена безопасна
	if len(h.subs) != 0 || h.stop != nil {
		t.Fatal("сверка должна остановиться без подписчиков")
	}
}

func TestDeckHubSlowSubscriberKeepsLatest(t *testing.T) {
	n := 0
	h := newDeckHub(func() deckStateData { n++; return deckStateData{Rev: n} })
	ch, cancel := h.subscribe()
	defer cancel()
	for i := 0; i < 20; i++ {
		h.check()
	}
	var last deckStateData
	for len(ch) > 0 {
		_ = json.Unmarshal(<-ch, &last)
	}
	if last.Rev < 20 {
		t.Fatalf("последний кадр потерян: %+v", last)
	}
}
