package twitch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type mem struct {
	set  map[string]string
	accs map[string]Account
}

func newMem() *mem                                    { return &mem{set: map[string]string{}, accs: map[string]Account{}} }
func (m *mem) Setting(k string) (string, bool, error) { v, ok := m.set[k]; return v, ok, nil }
func (m *mem) SetSetting(k, v string) error           { m.set[k] = v; return nil }
func (m *mem) ListAccounts() (o []Account, _ error) {
	for _, a := range m.accs {
		o = append(o, a)
	}
	return
}
func (m *mem) GetAccount(r string) (*Account, error) {
	a, ok := m.accs[r]
	if !ok {
		return nil, nil
	}
	return &a, nil
}
func (m *mem) SaveAccount(a Account) error  { m.accs[a.Role] = a; return nil }
func (m *mem) DeleteAccount(r string) error { delete(m.accs, r); return nil }

type fake struct {
	srv       *httptest.Server
	refreshes atomic.Int32
	revoked   atomic.Int32
	failRef   atomic.Bool
}

func newFake(t *testing.T) *fake {
	f := &fake{}
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth2/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		switch r.Form.Get("grant_type") {
		case "authorization_code":
			if r.Form.Get("code") != "good-code" || r.Form.Get("client_secret") != "sec" || r.Form.Get("redirect_uri") == "" {
				w.WriteHeader(400)
				w.Write([]byte(`{"status":400,"message":"Invalid authorization code"}`))
				return
			}
			w.Write([]byte(`{"access_token":"acc1","refresh_token":"ref1","expires_in":14000,"scope":["chat:read","chat:edit"],"token_type":"bearer"}`))
		case "refresh_token":
			f.refreshes.Add(1)
			if f.failRef.Load() || r.Form.Get("refresh_token") == "" {
				w.WriteHeader(400)
				w.Write([]byte(`{"status":400,"message":"Invalid refresh token"}`))
				return
			}
			w.Write([]byte(`{"access_token":"acc2","refresh_token":"ref2","expires_in":14000}`))
		default:
			w.WriteHeader(400)
		}
	})
	mux.HandleFunc("/oauth2/validate", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "OAuth acc") {
			w.WriteHeader(401)
			return
		}
		w.Write([]byte(`{"client_id":"cid","login":"somestreamer","scopes":["chat:read","chat:edit"],"user_id":"42","expires_in":14000}`))
	})
	mux.HandleFunc("/oauth2/revoke", func(w http.ResponseWriter, r *http.Request) { f.revoked.Add(1) })
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func setup(t *testing.T) (*Manager, *mem, *fake) {
	f, st := newFake(t), newMem()
	m := New(st)
	m.idBase = f.srv.URL
	if err := m.SetApp("cid", "sec"); err != nil {
		t.Fatal(err)
	}
	return m, st, f
}

const redir = "http://localhost:8000/auth/twitch/callback"

func connect(t *testing.T, m *Manager, role string) *Account {
	u, err := m.AuthURL(role, redir)
	if err != nil {
		t.Fatal(err)
	}
	pu, _ := url.Parse(u)
	acc, err := m.Callback(context.Background(), pu.Query().Get("state"), "good-code", redir)
	if err != nil {
		t.Fatal(err)
	}
	return acc
}

func TestAuthURL(t *testing.T) {
	m, _, f := setup(t)
	u, err := m.AuthURL(RoleBot, redir)
	if err != nil {
		t.Fatal(err)
	}
	pu, _ := url.Parse(u)
	q := pu.Query()
	if !strings.HasPrefix(u, f.srv.URL+"/oauth2/authorize?") || q.Get("client_id") != "cid" || q.Get("redirect_uri") != redir ||
		q.Get("response_type") != "code" || q.Get("scope") != "chat:read chat:edit" || q.Get("state") == "" || q.Get("force_verify") != "true" {
		t.Fatalf("bad url: %s", u)
	}
	if _, err := m.AuthURL("nobody", redir); err != ErrBadRole {
		t.Fatalf("role: %v", err)
	}
	m2 := New(newMem())
	if _, err := m2.AuthURL(RoleBot, redir); err != ErrNotConfigured {
		t.Fatalf("not configured: %v", err)
	}
}

func TestCallbackFlow(t *testing.T) {
	m, st, _ := setup(t)
	acc := connect(t, m, RoleStreamer)
	if acc.Login != "somestreamer" || acc.UserID != "42" || acc.Role != RoleStreamer || len(acc.Scopes) != 2 {
		t.Fatalf("account: %+v", acc)
	}
	if st.accs[RoleStreamer].AccessToken != "acc1" || st.accs[RoleStreamer].RefreshToken != "ref1" {
		t.Fatal("tokens not stored")
	}
	// токены не попадают в JSON
	b, _ := json.Marshal(acc)
	if strings.Contains(string(b), "acc1") || strings.Contains(string(b), "ref1") {
		t.Fatalf("tokens leaked: %s", b)
	}
	// чужой state и повтор state
	if _, err := m.Callback(context.Background(), "nope", "good-code", redir); err != ErrBadState {
		t.Fatalf("bad state: %v", err)
	}
	u, _ := m.AuthURL(RoleBot, redir)
	pu, _ := url.Parse(u)
	state := pu.Query().Get("state")
	if _, err := m.Callback(context.Background(), state, "bad-code", redir); err == nil {
		t.Fatal("bad code accepted")
	}
	if _, err := m.Callback(context.Background(), state, "good-code", redir); err != ErrBadState {
		t.Fatalf("state reuse: %v", err)
	}
	// просроченный state
	u, _ = m.AuthURL(RoleBot, redir)
	pu, _ = url.Parse(u)
	m.now = func() time.Time { return time.Now().Add(stateTTL + time.Minute) }
	if _, err := m.Callback(context.Background(), pu.Query().Get("state"), "good-code", redir); err != ErrBadState {
		t.Fatalf("expired state: %v", err)
	}
}

func TestTokenRefresh(t *testing.T) {
	m, st, f := setup(t)
	connect(t, m, RoleBot)
	ctx := context.Background()
	tok, login, err := m.Token(ctx, RoleBot)
	if err != nil || tok != "acc1" || login != "somestreamer" || f.refreshes.Load() != 0 {
		t.Fatalf("fresh token: %q %v refreshes=%d", tok, err, f.refreshes.Load())
	}
	// токен скоро истекает → обновляется, refresh-токен ротируется
	m.now = func() time.Time { return time.Now().Add(14000*time.Second - time.Minute) }
	tok, _, err = m.Token(ctx, RoleBot)
	if err != nil || tok != "acc2" || f.refreshes.Load() != 1 || st.accs[RoleBot].RefreshToken != "ref2" {
		t.Fatalf("refresh: %q %v", tok, err)
	}
	// refresh недействителен → ошибка и пометка «переподключить»
	f.failRef.Store(true)
	m.now = func() time.Time { return time.Now().Add(2 * 14000 * time.Second) }
	if _, _, err := m.Token(ctx, RoleBot); err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(st.accs[RoleBot].LastError, "reconnect") {
		t.Fatalf("last error: %q", st.accs[RoleBot].LastError)
	}
	if _, _, err := m.Token(ctx, RoleStreamer); err != ErrNoAccount {
		t.Fatalf("no account: %v", err)
	}
}

func TestDisconnectAndAppChange(t *testing.T) {
	m, st, f := setup(t)
	connect(t, m, RoleStreamer)
	connect(t, m, RoleBot)
	if err := m.Disconnect(context.Background(), RoleBot); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.accs[RoleBot]; ok || f.revoked.Load() != 1 {
		t.Fatalf("disconnect: revoked=%d", f.revoked.Load())
	}
	// тот же Client ID и пустой секрет — секрет и аккаунты на месте
	if err := m.SetApp("cid", ""); err != nil || st.set[keyClientSecret] != "sec" || len(st.accs) != 1 {
		t.Fatalf("same app: %v", err)
	}
	// другой Client ID — аккаунты сбрасываются
	if err := m.SetApp("other", "sec2"); err != nil || len(st.accs) != 0 {
		t.Fatalf("new app: %v accs=%d", err, len(st.accs))
	}
	if id, has, _ := m.AppInfo(); id != "other" || !has {
		t.Fatal("app info")
	}
	if err := m.SetApp("", ""); err == nil {
		t.Fatal("empty id accepted")
	}
}
