package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"

	"github.com/compico/overlaycomposer/internal/auth"
)

type memAuthStore struct {
	set  map[string]string
	devs map[string]auth.Device
}

func (m *memAuthStore) Setting(k string) (string, bool, error)      { v, ok := m.set[k]; return v, ok, nil }
func (m *memAuthStore) SetSetting(k, v string) error                { m.set[k] = v; return nil }
func (m *memAuthStore) ListDevices() ([]auth.Device, error)         { return nil, nil }
func (m *memAuthStore) SaveDevice(d auth.Device) error              { m.devs[d.ID] = d; return nil }
func (m *memAuthStore) DeleteDevice(id string) (bool, error)        { delete(m.devs, id); return true, nil }
func (m *memAuthStore) TouchDevice(string, string, time.Time) error { return nil }

// app.Test подставляет не-loopback адрес клиента, т.е. запросы выглядят как «извне».
func newAuthApp(t *testing.T) (*fiber.App, *auth.Manager) {
	m, err := auth.New(&memAuthStore{set: map[string]string{}, devs: map[string]auth.Device{}})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{d: Deps{Auth: m}}
	s.app = fiber.New()
	s.app.Use(s.authGuard)
	s.authRoutes()
	s.app.Get("/api/state", func(c fiber.Ctx) error { return c.SendString("secret") })
	s.app.Get("/editor", func(c fiber.Ctx) error { return c.SendString("editor") })
	s.app.Get("/plugins/x/", func(c fiber.Ctx) error { return c.SendString("plugin") })
	return s.app, m
}

func do(t *testing.T, app *fiber.App, req *http.Request) (int, string, *http.Response) {
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return resp.StatusCode, sb.String(), resp
}

func TestRemoteNeedsToken(t *testing.T) {
	app, m := newAuthApp(t)

	if code, _, _ := do(t, app, httptest.NewRequest("GET", "/api/state", nil)); code != 401 {
		t.Fatalf("api without token: %d", code)
	}
	req := httptest.NewRequest("GET", "/editor", nil)
	req.Header.Set("Accept", "text/html")
	if code, _, resp := do(t, app, req); code != 303 && code != 302 || resp.Header.Get("Location") != "/pair" {
		t.Fatalf("page without token: %d %q", code, resp.Header.Get("Location"))
	}
	if code, body, _ := do(t, app, httptest.NewRequest("GET", "/pair", nil)); code != 200 || !strings.Contains(body, "Connect") {
		t.Fatalf("pair page: %d", code)
	}
	if code, _, _ := do(t, app, httptest.NewRequest("GET", "/plugins/x/", nil)); code != 200 {
		t.Fatalf("plugin files must be public: %d", code)
	}

	// неверный код
	post := func(code string) (int, string, *http.Response) {
		r := httptest.NewRequest("POST", "/api/pair", strings.NewReader(`{"code":"`+code+`","name":"iPad"}`))
		r.Header.Set("Content-Type", "application/json")
		return do(t, app, r)
	}
	if code, _, _ := post("AAAAAA"); code != 403 {
		t.Fatalf("bad code: %d", code)
	}
	good, _ := m.Pairing()
	code, body, resp := post(good)
	if code != 200 || !strings.Contains(body, `"token"`) {
		t.Fatalf("pair: %d %s", code, body)
	}
	var cookie string
	for _, c := range resp.Cookies() {
		if c.Name == tokenCookie {
			cookie = c.Value
			if !c.HttpOnly {
				t.Fatal("cookie must be HttpOnly")
			}
		}
	}
	if cookie == "" {
		t.Fatal("no cookie")
	}

	// cookie и Bearer открывают доступ
	req = httptest.NewRequest("GET", "/api/state", nil)
	req.AddCookie(&http.Cookie{Name: tokenCookie, Value: cookie})
	if code, body, _ := do(t, app, req); code != 200 || body != "secret" {
		t.Fatalf("with cookie: %d", code)
	}
	req = httptest.NewRequest("GET", "/api/state", nil)
	req.Header.Set("Authorization", "Bearer "+cookie)
	if code, _, _ := do(t, app, req); code != 200 {
		t.Fatalf("with bearer: %d", code)
	}
	// управление устройствами — только локально, даже для сопряжённого устройства
	req = httptest.NewRequest("GET", "/api/devices", nil)
	req.AddCookie(&http.Cookie{Name: tokenCookie, Value: cookie})
	if code, _, _ := do(t, app, req); code != 403 {
		t.Fatalf("devices from paired remote: %d", code)
	}
	// отзыв
	devs := m.Devices()
	if len(devs) != 1 {
		t.Fatalf("devices: %d", len(devs))
	}
	if ok, _ := m.Revoke(devs[0].ID); !ok {
		t.Fatal("revoke")
	}
	req = httptest.NewRequest("GET", "/api/state", nil)
	req.Header.Set("Authorization", "Bearer "+cookie)
	if code, _, _ := do(t, app, req); code != 401 {
		t.Fatalf("revoked: %d", code)
	}
}

// sad path: сканер без токена после лимита получает 429, а устройство с токеном не страдает.
func TestUnauthorizedFloodIsLimited(t *testing.T) {
	app, m := newAuthApp(t)
	s := &Server{d: Deps{Auth: m}, deny: newDenyLimiter(5, time.Minute)}
	app = fiber.New()
	app.Use(s.authGuard)
	s.app = app
	s.authRoutes()
	app.Get("/api/state", func(c fiber.Ctx) error { return c.SendString("secret") })

	var codes []int
	for i := 0; i < 8; i++ {
		code, _, resp := do(t, app, httptest.NewRequest("GET", "/api/state", nil))
		codes = append(codes, code)
		if code == 429 && resp.Header.Get("Retry-After") == "" {
			t.Error("429 без Retry-After")
		}
	}
	want := []int{401, 401, 401, 401, 401, 429, 429, 429}
	for i := range want {
		if codes[i] != want[i] {
			t.Fatalf("codes = %v, want %v", codes, want)
		}
	}
	// устройство с токеном проходит, даже когда адрес уже в лимите
	good, _ := m.Pairing()
	r := httptest.NewRequest("POST", "/api/pair", strings.NewReader(`{"code":"`+good+`"}`))
	r.Header.Set("Content-Type", "application/json")
	_, body, _ := do(t, app, r)
	tok := body[strings.Index(body, `"token":"`)+9:]
	tok = tok[:strings.Index(tok, `"`)]
	req := httptest.NewRequest("GET", "/api/state", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	if code, b, _ := do(t, app, req); code != 200 || b != "secret" {
		t.Fatalf("с токеном: %d %q", code, b)
	}
}

func TestDenyLimiterWindowResets(t *testing.T) {
	l := newDenyLimiter(2, time.Minute)
	now := time.Now()
	l.now = func() time.Time { return now }
	if !l.allow("a") || !l.allow("a") || l.allow("a") {
		t.Fatal("третий отказ в окне должен быть остановлен")
	}
	if !l.allow("b") {
		t.Fatal("другой адрес не должен страдать")
	}
	now = now.Add(61 * time.Second)
	if !l.allow("a") {
		t.Fatal("после окна счётчик сбрасывается")
	}
}
