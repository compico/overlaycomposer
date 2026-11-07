package twitch

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type helixCall struct {
	Method, Path, Query, Auth, ClientID string
	Body                                map[string]any
}

type helixFake struct {
	srv   *httptest.Server
	mu    sync.Mutex
	calls []helixCall
	// manageable — id наград, созданных «нашим» приложением
	manageable map[string]bool
}

func newHelixFake(t *testing.T) *helixFake {
	f := &helixFake{manageable: map[string]bool{"mine": true}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		c := helixCall{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Auth: r.Header.Get("Authorization"), ClientID: r.Header.Get("Client-Id")}
		_ = json.Unmarshal(raw, &c.Body)
		f.mu.Lock()
		f.calls = append(f.calls, c)
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "GET" && r.URL.Path == "/helix/channel_points/custom_rewards":
			if r.URL.Query().Get("only_manageable_rewards") == "true" {
				_, _ = w.Write([]byte(`{"data":[{"id":"mine","title":"Track","cost":500,"is_enabled":true}]}`))
				return
			}
			_, _ = w.Write([]byte(`{"data":[{"id":"mine","title":"Track","cost":500,"prompt":"link","is_user_input_required":true,"is_enabled":true},{"id":"other","title":"Hydrate","cost":100,"is_enabled":false}]}`))
		case r.Method == "POST" && r.URL.Path == "/helix/channel_points/custom_rewards":
			_, _ = w.Write([]byte(`{"data":[{"id":"new1","title":"Order","cost":300,"is_user_input_required":true,"is_enabled":true}]}`))
		case r.Method == "PATCH" && r.URL.Path == "/helix/channel_points/custom_rewards/redemptions":
			if !f.manageable[r.URL.Query().Get("reward_id")] {
				w.WriteHeader(403)
				_, _ = w.Write([]byte(`{"message":"The broadcaster is not the owner of this reward"}`))
				return
			}
			_, _ = w.Write([]byte(`{"data":[{}]}`))
		case r.Method == "POST" && r.URL.Path == "/helix/eventsub/subscriptions":
			w.WriteHeader(202)
			_, _ = w.Write([]byte(`{"data":[{}]}`))
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *helixFake) last() helixCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[len(f.calls)-1]
}

func (f *helixFake) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// newRewardsManager — менеджер с подключённым стримером (id 42), указывающий на поддельный Helix.
func newRewardsManager(t *testing.T, scopes ...string) (*Manager, *helixFake, *mem) {
	t.Helper()
	f := newHelixFake(t)
	st := newMem()
	st.set[keyClientID] = "cid"
	st.set[keyClientSecret] = "sec"
	if scopes == nil {
		scopes = []string{ScopeReadRedemptions, ScopeManageRedemptions}
	}
	st.accs[RoleStreamer] = Account{Role: RoleStreamer, UserID: "42", Login: "me", Scopes: scopes, AccessToken: "tok", ExpiresAt: time.Now().Add(time.Hour)}
	m := New(st)
	m.UseAPIBase(f.srv.URL)
	return m, f, st
}

func TestRewardsMarksManageable(t *testing.T) {
	m, f, _ := newRewardsManager(t)
	rs, err := m.Rewards(context.Background())
	if err != nil || len(rs) != 2 {
		t.Fatalf("%v %+v", err, rs)
	}
	if !rs[0].Manageable || rs[0].ID != "mine" || !rs[0].InputRequired || rs[1].Manageable || rs[1].Enabled {
		t.Fatalf("%+v", rs)
	}
	c := f.last()
	if c.Auth != "Bearer tok" || c.ClientID != "cid" {
		t.Fatalf("заголовки: %+v", c)
	}
	if f.calls[0].Query != "broadcaster_id=42" {
		t.Fatalf("broadcaster_id должен браться из аккаунта: %q", f.calls[0].Query)
	}
}

func TestCreateRewardAndUpdateRedemption(t *testing.T) {
	m, f, _ := newRewardsManager(t)
	r, err := m.CreateReward(context.Background(), NewReward{Title: " Order ", Cost: 300, Prompt: "link", InputRequired: true})
	if err != nil || r.ID != "new1" || !r.Manageable {
		t.Fatalf("%v %+v", err, r)
	}
	if b := f.last().Body; b["title"] != "Order" || b["cost"] != float64(300) || b["is_user_input_required"] != true {
		t.Fatalf("тело: %+v", b)
	}
	if err := m.UpdateRedemption(context.Background(), "mine", "red1", RedemptionFulfilled); err != nil {
		t.Fatal(err)
	}
	c := f.last()
	if c.Method != "PATCH" || c.Query != "broadcaster_id=42&id=red1&reward_id=mine" || c.Body["status"] != "FULFILLED" {
		t.Fatalf("%+v", c)
	}
}

func TestRewardsSadPaths(t *testing.T) {
	ctx := context.Background()
	m, f, _ := newRewardsManager(t)
	for name, n := range map[string]NewReward{
		"пустое название":       {Title: " ", Cost: 100},
		"длинное название":      {Title: strings.Repeat("ж", 46), Cost: 100},
		"нулевая стоимость":     {Title: "A", Cost: 0},
		"слишком длинный текст": {Title: "A", Cost: 1, Prompt: strings.Repeat("ж", 201)},
	} {
		if _, err := m.CreateReward(ctx, n); err == nil {
			t.Errorf("%s: ошибка ожидалась", name)
		}
	}
	if f.count() != 0 {
		t.Fatalf("при отказе запросов к Twitch быть не должно: %d", f.count())
	}
	// чужая награда: Twitch отвечает 403 — ошибка доходит как есть
	if err := m.UpdateRedemption(ctx, "other", "r", RedemptionCanceled); err == nil {
		t.Fatal("для чужой награды ожидалась ошибка")
	}
	if err := m.UpdateRedemption(ctx, "mine", "r", "PENDING"); err == nil {
		t.Fatal("неизвестный статус")
	}
	if err := m.UpdateRedemption(ctx, "", "r", RedemptionFulfilled); err == nil {
		t.Fatal("пустой id награды")
	}
}

func TestRewardsNeedScopesAndAccount(t *testing.T) {
	m, f, _ := newRewardsManager(t, "chat:read", "chat:edit") // старое подключение без прав на награды
	if _, err := m.Rewards(context.Background()); !errors.Is(err, ErrNeedScopes) {
		t.Fatalf("ожидалась ErrNeedScopes: %v", err)
	}
	m2 := New(newMem())
	if _, err := m2.Rewards(context.Background()); !errors.Is(err, ErrNoAccount) {
		t.Fatalf("ожидалась ErrNoAccount: %v", err)
	}
	if f.count() != 0 {
		t.Fatal("без прав запросов к Twitch быть не должно")
	}
}
