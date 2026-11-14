package twitch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// fakeDevice — id.twitch.tv для Device Code Grant Flow: сначала authorization_pending, потом токены.
type fakeDevice struct {
	srv        *httptest.Server
	pending    atomic.Int32 // сколько раз ответить authorization_pending
	denyWith   string
	sawSecret  atomic.Bool
	deviceReqs atomic.Int32
}

func newFakeDevice(t *testing.T) *fakeDevice {
	f := &fakeDevice{}
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth2/device", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		f.deviceReqs.Add(1)
		if r.Form.Get("client_id") == "" || r.Form.Get("scopes") == "" {
			w.WriteHeader(400)
			w.Write([]byte(`{"status":400,"message":"missing"}`))
			return
		}
		w.Write([]byte(`{"device_code":"dev1","user_code":"ABCDEFGH","verification_uri":"https://www.twitch.tv/activate?device-code=ABCDEFGH","expires_in":1800,"interval":0}`))
	})
	mux.HandleFunc("/oauth2/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.Form.Get("client_secret") != "" {
			f.sawSecret.Store(true)
		}
		switch r.Form.Get("grant_type") {
		case "urn:ietf:params:oauth:grant-type:device_code":
			if r.Form.Get("device_code") != "dev1" {
				w.WriteHeader(400)
				w.Write([]byte(`{"status":400,"message":"invalid device code"}`))
				return
			}
			if f.denyWith != "" {
				w.WriteHeader(400)
				w.Write([]byte(`{"status":400,"message":"` + f.denyWith + `"}`))
				return
			}
			if f.pending.Add(-1) >= 0 {
				w.WriteHeader(400)
				w.Write([]byte(`{"status":400,"message":"authorization_pending"}`))
				return
			}
			w.Write([]byte(`{"access_token":"acc1","refresh_token":"ref1","expires_in":14000,"scope":["chat:read"],"token_type":"bearer"}`))
		case "refresh_token":
			w.Write([]byte(`{"access_token":"acc2","refresh_token":"ref2","expires_in":14000}`))
		}
	})
	mux.HandleFunc("/oauth2/validate", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"user_id":"42","login":"streamer","scopes":["chat:read","chat:edit","channel:read:redemptions","channel:manage:redemptions"]}`))
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func newPublicManager(t *testing.T, f *fakeDevice) (*Manager, *mem) {
	t.Helper()
	old := DefaultClientID
	DefaultClientID = "builtin-id"
	t.Cleanup(func() { DefaultClientID = old })
	st := newMem()
	m := New(st)
	m.UseBaseURL(f.srv.URL)
	m.pollMin = time.Millisecond
	return m, st
}

func waitState(t *testing.T, m *Manager, flow string) DeviceState {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if s, ok := m.DeviceStatus(flow); ok && s.Status != DevicePending {
			return s
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("подключение не завершилось")
	return DeviceState{}
}

func TestDeviceFlowConnects(t *testing.T) {
	f := newFakeDevice(t)
	f.pending.Store(2)
	m, st := newPublicManager(t, f)
	var connected atomic.Int32
	m.OnConnect(func(a Account) { connected.Add(1) })

	if mode, _ := m.Mode(); mode != "device" {
		t.Fatalf("mode = %q, ждали device (встроенное приложение без секрета)", mode)
	}
	start, err := m.StartDevice(context.Background(), RoleStreamer)
	if err != nil {
		t.Fatal(err)
	}
	if start.UserCode != "ABCDEFGH" || start.VerificationURI == "" || start.Flow == "" {
		t.Fatalf("неполный ответ: %+v", start)
	}
	if s, _ := m.DeviceStatus(start.Flow); s.Status != DevicePending {
		t.Fatalf("сразу после старта: %+v", s)
	}
	s := waitState(t, m, start.Flow)
	if s.Status != DeviceDone || s.Login != "streamer" {
		t.Fatalf("итог: %+v", s)
	}
	acc := st.accs[RoleStreamer]
	if acc.Login != "streamer" || acc.AccessToken != "acc1" || acc.RefreshToken != "ref1" || len(MissingScopes(RoleStreamer, acc.Scopes)) != 0 {
		t.Fatalf("аккаунт: %+v", acc)
	}
	if connected.Load() != 1 {
		t.Fatalf("OnConnect вызван %d раз", connected.Load())
	}
	if f.sawSecret.Load() {
		t.Fatal("Public-приложение не должно отправлять client_secret")
	}
}

func TestDeviceFlowDenied(t *testing.T) {
	f := newFakeDevice(t)
	f.denyWith = "access_denied"
	m, st := newPublicManager(t, f)
	start, err := m.StartDevice(context.Background(), RoleBot)
	if err != nil {
		t.Fatal(err)
	}
	s := waitState(t, m, start.Flow)
	if s.Status != DeviceFailed || s.Error != "access was denied on Twitch" {
		t.Fatalf("итог: %+v", s)
	}
	if len(st.accs) != 0 {
		t.Fatal("аккаунт не должен сохраниться")
	}
}

func TestDeviceFlowRestartCancelsPrevious(t *testing.T) {
	f := newFakeDevice(t)
	f.pending.Store(1 << 20) // «вечное» ожидание
	m, _ := newPublicManager(t, f)
	a, _ := m.StartDevice(context.Background(), RoleStreamer)
	b, _ := m.StartDevice(context.Background(), RoleStreamer)
	if _, ok := m.DeviceStatus(a.Flow); ok {
		t.Fatal("старое подключение той же роли должно быть отменено и забыто")
	}
	if s, ok := m.DeviceStatus(b.Flow); !ok || s.Status != DevicePending {
		t.Fatalf("новое: %+v %v", s, ok)
	}
}

func TestDeviceNeedsAppAndNoSecret(t *testing.T) {
	f := newFakeDevice(t)
	st := newMem()
	m := New(st)
	m.UseBaseURL(f.srv.URL)
	old := DefaultClientID
	DefaultClientID = ""
	t.Cleanup(func() { DefaultClientID = old })
	if _, err := m.StartDevice(context.Background(), RoleStreamer); err == nil {
		t.Fatal("без приложения должна быть ошибка")
	}
	if mode, _ := m.Mode(); mode != "" {
		t.Fatalf("mode = %q", mode)
	}
	if _, err := m.StartDevice(context.Background(), "nobody"); err != ErrBadRole {
		t.Fatalf("роль: %v", err)
	}
	if err := m.SetApp("own-id", "own-secret"); err != nil {
		t.Fatal(err)
	}
	if mode, _ := m.Mode(); mode != "code" {
		t.Fatalf("mode = %q, ждали code (своё приложение с секретом)", mode)
	}
	if _, err := m.StartDevice(context.Background(), RoleStreamer); err == nil {
		t.Fatal("со своим секретом подключение по коду не используется")
	}
}

func TestPublicRefreshOmitsSecret(t *testing.T) {
	f := newFakeDevice(t)
	m, st := newPublicManager(t, f)
	st.accs[RoleStreamer] = Account{Role: RoleStreamer, Login: "s", AccessToken: "old", RefreshToken: "ref1", ExpiresAt: time.Now().Add(-time.Hour)}
	tok, _, err := m.Token(context.Background(), RoleStreamer)
	if err != nil || tok != "acc2" {
		t.Fatalf("token=%q err=%v", tok, err)
	}
	if f.sawSecret.Load() {
		t.Fatal("обновление токена Public-приложения не должно слать client_secret")
	}
	if st.accs[RoleStreamer].RefreshToken != "ref2" {
		t.Fatal("новый refresh-токен должен сохраниться (ротация)")
	}
}

func TestResetAppReturnsToBuiltIn(t *testing.T) {
	f := newFakeDevice(t)
	m, st := newPublicManager(t, f)
	_ = m.SetApp("own-id", "own-secret")
	st.accs[RoleBot] = Account{Role: RoleBot}
	if err := m.ResetApp(); err != nil {
		t.Fatal(err)
	}
	id, hasSecret, _ := m.AppInfo()
	if id != "" || hasSecret || len(st.accs) != 0 {
		t.Fatalf("после сброса: id=%q secret=%v accounts=%d", id, hasSecret, len(st.accs))
	}
	if mode, _ := m.Mode(); mode != "device" {
		t.Fatalf("mode = %q", mode)
	}
}
