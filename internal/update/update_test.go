package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestEnabled(t *testing.T) {
	for _, tc := range []struct {
		ch, v string
		want  bool
	}{
		{"release", "v2026.10.27.5", true},
		{"", "v2026.10.27.5", false},                     // локальная сборка с тегом
		{"release", "dev", false},                        // нет версии
		{"release", "v2026.10.27.5-3-gabc-dirty", false}, // сборка не на теге
	} {
		if got := Enabled(tc.ch, tc.v); got != tc.want {
			t.Errorf("Enabled(%q,%q)=%v", tc.ch, tc.v, got)
		}
	}
}

func TestNewer(t *testing.T) {
	cases := []struct {
		l, c string
		want bool
	}{
		{"v2026.10.27.6", "v2026.10.27.5", true},
		{"v2026.10.27.5", "v2026.10.27.5", false},
		{"v2026.10.27.5", "v2026.10.27.12", false}, // числовое, не строковое сравнение
		{"v2026.11.01.1", "v2026.10.31.99", true},
		{"garbage", "v2026.10.27.5", false},
		{"v2026.10.27.5", "dev", false},
	}
	for _, tc := range cases {
		if got := Newer(tc.l, tc.c); got != tc.want {
			t.Errorf("Newer(%q,%q)=%v", tc.l, tc.c, got)
		}
	}
}

func fakeAPI(t *testing.T, status int, tag string) (*httptest.Server, *atomic.Int32) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.WriteHeader(status)
		if status == 200 {
			w.Write([]byte(`{"tag_name":"` + tag + `","html_url":"https://github.com/x/y/releases/tag/` + tag + `","body":"notes"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &n
}

func TestCheckThrottlesAcrossRestarts(t *testing.T) {
	srv, hits := fakeAPI(t, 200, "v2026.10.28.1")
	state := filepath.Join(t.TempDir(), "update.json")
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	mk := func() *Checker { // каждый «запуск» — новый Checker с тем же файлом состояния
		return &Checker{Current: "v2026.10.27.5", StateFile: state, API: srv.URL, Now: func() time.Time { return now }}
	}
	i, err := mk().Check(context.Background())
	if err != nil || !i.Available || i.Latest != "v2026.10.28.1" {
		t.Fatalf("first: %+v %v", i, err)
	}
	for k := 0; k < 20; k++ { // crashloop
		i, err = mk().Check(context.Background())
		if err != nil || !i.Available {
			t.Fatalf("cached: %+v %v", i, err)
		}
	}
	if hits.Load() != 1 {
		t.Fatalf("запросов к API: %d, ожидался 1", hits.Load())
	}
	now = now.Add(MinInterval + time.Minute)
	mk().Check(context.Background())
	if hits.Load() != 2 {
		t.Fatalf("после интервала должен быть 2-й запрос, а их %d", hits.Load())
	}
}

func TestCheckFailureBacksOff(t *testing.T) {
	srv, hits := fakeAPI(t, 403, "")
	state := filepath.Join(t.TempDir(), "update.json")
	now := time.Now()
	for k := 0; k < 10; k++ {
		c := &Checker{Current: "v2026.10.27.5", StateFile: state, API: srv.URL, Now: func() time.Time { return now }}
		c.Check(context.Background())
	}
	if hits.Load() != 1 {
		t.Fatalf("после ошибки повторные запуски не должны ходить в сеть: %d", hits.Load())
	}
	now = now.Add(ErrorInterval + time.Minute)
	c := &Checker{Current: "v2026.10.27.5", StateFile: state, API: srv.URL, Now: func() time.Time { return now }}
	if _, err := c.Check(context.Background()); err == nil || hits.Load() != 2 {
		t.Fatalf("через ErrorInterval ожидалась повторная попытка (hits=%d err=%v)", hits.Load(), err)
	}
}

func TestCheckRejectsOddTagAndKeepsCache(t *testing.T) {
	srv, _ := fakeAPI(t, 200, "nightly")
	c := &Checker{Current: "v2026.10.27.5", StateFile: filepath.Join(t.TempDir(), "u.json"), API: srv.URL}
	if i, err := c.Check(context.Background()); err == nil || i.Available {
		t.Fatalf("странный тег должен отвергаться: %+v %v", i, err)
	}
}
