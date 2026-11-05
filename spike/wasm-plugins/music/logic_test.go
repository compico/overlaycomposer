package main

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

type fakeHost struct {
	kv      map[string]string
	updates []string
	logs    []string
}

func (f *fakeHost) Get(k string) string               { return f.kv[k] }
func (f *fakeHost) Set(k, v string)                   { f.kv[k] = v }
func (f *fakeHost) Log(l, m string, _ map[string]any) { f.logs = append(f.logs, m) }
func (f *fakeHost) UpdateRedemption(_, id, st string) { f.updates = append(f.updates, id+":"+st) }

func newP() (*Plugin, *fakeHost) {
	h := &fakeHost{kv: map[string]string{}}
	return &Plugin{H: h, Cfg: Config{RewardID: "rw"}, Now: func() time.Time { return time.Unix(1000, 0) }}, h
}

func red(id, in string) Redemption { return Redemption{ID: id, RewardID: "rw", User: "v", Input: in} }
func link(n int) string            { return fmt.Sprintf("https://music.yandex.ru/album/%d/track/%d", n, n*10) }
func body(id int) json.RawMessage  { return json.RawMessage(fmt.Sprintf(`{"id":%d}`, id)) }

func item(t *testing.T, r Response) (map[string]any, bool) {
	t.Helper()
	m := r.Body.(map[string]any)
	it, _ := m["item"].(Item)
	if m["item"] == nil {
		return nil, false
	}
	b, _ := json.Marshal(it)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return out, m["resend"] == true
}

func TestParseYandex(t *testing.T) {
	if a, tr, ok := parseYandex("  https://MUSIC.yandex.com/album/1/track/2/?utm=x "); !ok || a != "1" || tr != "2" {
		t.Fatal("валидная ссылка не принята")
	}
	for _, bad := range []string{"", "http://music.yandex.ru/album/1/track/2", "https://evil.com/album/1/track/2",
		"https://music.yandex.ru.evil.com/album/1/track/2", "https://u@music.yandex.ru/album/1/track/2",
		"https://music.yandex.ru/album/1", "see https://music.yandex.ru/album/1/track/2"} {
		if _, _, ok := parseYandex(bad); ok {
			t.Errorf("%q принят", bad)
		}
	}
}

func TestFIFOPollingAndConfirmedStart(t *testing.T) {
	p, h := newP()
	p.OnRedemption(red("r1", link(1)))
	p.OnRedemption(red("r2", link(2)))
	it, resend := item(t, p.Handle("next", Request{}))
	if it["id"] != float64(1) || resend {
		t.Fatalf("первой должна уйти первая: %v", it)
	}
	// повторный опрос идемпотентен: та же заявка, с resend, очередь не продвигается
	it, resend = item(t, p.Handle("next", Request{}))
	if it["id"] != float64(1) || !resend {
		t.Fatalf("%v resend=%v", it, resend)
	}
	if r := p.Handle("started", Request{Body: body(1)}); r.Status != 200 {
		t.Fatalf("%+v", r)
	}
	it, resend = item(t, p.Handle("next", Request{}))
	if it["id"] != float64(2) || resend {
		t.Fatalf("после старта первой — вторая: %v", it)
	}
	if len(h.updates) != 1 || h.updates[0] != "r1:FULFILLED" {
		t.Fatalf("%v", h.updates)
	}
	if it, _ := item(t, p.Handle("next", Request{})); it == nil || it["id"] != float64(2) {
		t.Fatal("вторая всё ещё отдана и не подтверждена")
	}
	p.Handle("started", Request{Body: body(2)})
	if it, _ := item(t, p.Handle("next", Request{})); it != nil {
		t.Fatalf("очередь пуста: %v", it)
	}
}

func TestDuplicateForeignAndRejected(t *testing.T) {
	p, h := newP()
	p.OnRedemption(red("r1", link(1)))
	p.OnRedemption(red("r1", link(1))) // повтор
	foreign := red("r2", link(2))
	foreign.RewardID = "other"
	p.OnRedemption(foreign)
	p.OnRedemption(red("r3", "https://evil.example/x"))
	p.OnRedemption(red("r3", "https://evil.example/x")) // повтор отклонённого: баллы второй раз не возвращаем
	s := p.load()
	if len(s.Items) != 2 || s.Items[1].Status != "rejected" {
		t.Fatalf("%+v", s.Items)
	}
	if len(h.updates) != 1 || h.updates[0] != "r3:CANCELED" {
		t.Fatalf("%v", h.updates)
	}
}

func TestSadPaths(t *testing.T) {
	p, _ := newP()
	p.OnRedemption(red("r1", link(1)))
	if r := p.Handle("started", Request{Body: body(1)}); r.Status != 409 {
		t.Fatalf("старт неотданной заявки: %d", r.Status)
	}
	if r := p.Handle("started", Request{Body: body(99)}); r.Status != 404 {
		t.Fatalf("неизвестная заявка: %d", r.Status)
	}
	for _, b := range []string{``, `not json`, `{}`, `{"id":0}`} {
		if r := p.Handle("started", Request{Body: json.RawMessage(b)}); r.Status != 400 {
			t.Errorf("тело %q: %d", b, r.Status)
		}
	}
	if r := p.Handle("nope", Request{}); r.Status != 404 {
		t.Fatalf("неизвестный обработчик: %d", r.Status)
	}
	p.Handle("next", Request{})
	p.Handle("started", Request{Body: body(1)})
	if r := p.Handle("started", Request{Body: body(1)}); r.Status != 409 {
		t.Fatalf("повторное подтверждение: %d", r.Status)
	}
}

func TestFailedCancelsAndMovesOn(t *testing.T) {
	p, h := newP()
	p.OnRedemption(red("r1", link(1)))
	p.OnRedemption(red("r2", link(2)))
	p.Handle("next", Request{})
	if r := p.Handle("failed", Request{Body: json.RawMessage(`{"id":1,"reason":"player not ready"}`)}); r.Status != 200 {
		t.Fatalf("%+v", r)
	}
	if h.updates[0] != "r1:CANCELED" {
		t.Fatalf("%v", h.updates)
	}
	if it, _ := item(t, p.Handle("next", Request{})); it == nil || it["id"] != float64(2) {
		t.Fatalf("очередь не должна вставать: %v", it)
	}
}

func TestQueueLimitAndPruning(t *testing.T) {
	p, h := newP()
	for i := 1; i <= maxQueued+1; i++ {
		p.OnRedemption(red(fmt.Sprintf("r%d", i), link(i)))
	}
	s := p.load()
	if last := s.Items[len(s.Items)-1]; last.Status != "rejected" || last.Error != "the queue is full" {
		t.Fatalf("%+v", last)
	}
	if len(h.updates) != 1 {
		t.Fatalf("%v", h.updates)
	}
	// история не растёт бесконечно: завершённые срезаются
	p2, _ := newP()
	for i := 1; i <= maxKept+50; i++ {
		p2.OnRedemption(red(fmt.Sprintf("x%d", i), "bad"))
	}
	if n := len(p2.load().Items); n > maxKept {
		t.Fatalf("история %d > %d", n, maxKept)
	}
}

func TestStateSurvivesNewInstance(t *testing.T) {
	p, h := newP()
	p.OnRedemption(red("r1", link(1)))
	p.Handle("next", Request{})
	// «перезапуск плагина»: новый экземпляр над тем же storage
	p2 := &Plugin{H: h, Cfg: p.Cfg, Now: p.Now}
	if it, resend := item(t, p2.Handle("next", Request{})); it == nil || !resend {
		t.Fatalf("активная заявка должна уйти повторно: %v %v", it, resend)
	}
}
