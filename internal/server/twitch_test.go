package server

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"

	"github.com/compico/overlaycomposer/internal/auth"
	"github.com/compico/overlaycomposer/internal/chat"
	"github.com/compico/overlaycomposer/internal/config"
	"github.com/compico/overlaycomposer/internal/twitch"
)

// Тест идёт по настоящему loopback-соединению, поэтому проверяет и ветку «запрос с этого компьютера».
func TestTwitchRoutesLocal(t *testing.T) {
	fakeTwitch := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth2/token":
			w.Write([]byte(`{"access_token":"acc1","refresh_token":"ref1","expires_in":14000}`))
		case "/oauth2/validate":
			w.Write([]byte(`{"login":"mystreamer","user_id":"7","scopes":["chat:read","chat:edit"]}`))
		}
	}))
	defer fakeTwitch.Close()
	tw := twitch.New(twitch.NewMemStorage())
	tw.UseBaseURL(fakeTwitch.URL)
	am, err := auth.New(&memAuthStore{set: map[string]string{}, devs: map[string]auth.Device{}})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{d: Deps{Auth: am, Twitch: tw, Chat: chat.NewWithDialer(tw, nil), Running: config.Config{Host: "127.0.0.1", Port: 8123}}}
	s.app = fiber.New()
	s.app.Use(s.authGuard)
	s.authRoutes()
	s.twitchRoutes()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go s.app.Listener(ln, fiber.ListenConfig{DisableStartupMessage: true})
	defer s.app.Shutdown()
	base := "http://" + ln.Addr().String()

	call := func(method, path, body string, hdr map[string]string) (int, string) {
		req, _ := http.NewRequest(method, base+path, strings.NewReader(body))
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	guard := map[string]string{"X-Overlay-Composer": "1"}

	// без служебного заголовка менять приложение нельзя
	if code, _ := call("PUT", "/api/twitch/app", `{"clientId":"cid","clientSecret":"topsecret"}`, nil); code != 400 {
		t.Fatalf("no guard header: %d", code)
	}
	code, body := call("PUT", "/api/twitch/app", `{"clientId":"cid","clientSecret":"topsecret"}`, guard)
	if code != 200 || strings.Contains(body, "topsecret") || !strings.Contains(body, `"hasSecret":true`) ||
		!strings.Contains(body, "http://localhost:8123/auth/twitch/callback") {
		t.Fatalf("set app: %d %s", code, body)
	}
	// запрос «через прокси» (есть X-Forwarded-For) — уже не локальный
	if code, _ := call("GET", "/api/twitch", "", map[string]string{"X-Forwarded-For": "1.2.3.4"}); code != 401 {
		t.Fatalf("forwarded request: %d", code)
	}
	if code, _ := call("POST", "/api/twitch/connect/nobody", "", guard); code != 400 {
		t.Fatalf("bad role: %d", code)
	}
	code, body = call("POST", "/api/twitch/connect/bot", "", guard)
	if code != 200 {
		t.Fatalf("connect: %d %s", code, body)
	}
	var cr struct{ URL string }
	if err := json.Unmarshal([]byte(body), &cr); err != nil {
		t.Fatal(err)
	}
	pu, err := url.Parse(cr.URL)
	if err != nil || pu.Query().Get("state") == "" {
		t.Fatalf("auth url %q: %v", cr.URL, err)
	}
	state := pu.Query().Get("state")

	if code, body := call("GET", "/auth/twitch/callback?code=x&state=wrong", "", nil); code != 400 || !strings.Contains(body, "Could not connect") {
		t.Fatalf("bad state: %d", code)
	}
	if code, body := call("GET", "/auth/twitch/callback?error=access_denied&error_description=<script>x</script>", "", nil); code != 400 || strings.Contains(body, "<script>x") {
		t.Fatalf("denied/escape: %d %s", code, body)
	}
	code, body = call("GET", "/auth/twitch/callback?code=good&state="+state, "", nil)
	if code != 200 || !strings.Contains(body, "mystreamer") {
		t.Fatalf("callback: %d %s", code, body)
	}
	code, body = call("GET", "/api/twitch", "", nil)
	if code != 200 || !strings.Contains(body, `"login":"mystreamer"`) || !strings.Contains(body, `"role":"bot"`) ||
		strings.Contains(body, "acc1") || strings.Contains(body, "ref1") || strings.Contains(body, "topsecret") {
		t.Fatalf("status: %d %s", code, body)
	}
	if code, _ := call("DELETE", "/api/twitch/accounts/bot", "", guard); code != 200 {
		t.Fatalf("disconnect: %d", code)
	}
	if _, body := call("GET", "/api/twitch", "", nil); strings.Contains(body, "mystreamer") {
		t.Fatalf("account still there: %s", body)
	}
}
