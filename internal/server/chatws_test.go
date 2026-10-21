package server

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fasthttp/websocket"
	fws "github.com/gofiber/contrib/v3/websocket"
	"github.com/gofiber/fiber/v3"

	"github.com/compico/overlaycomposer/internal/auth"
	"github.com/compico/overlaycomposer/internal/chat"
	"github.com/compico/overlaycomposer/internal/hub"
	"github.com/compico/overlaycomposer/internal/model"
	"github.com/compico/overlaycomposer/internal/twitch"
)

// ---- заглушки ----

type stubStore struct{}

func (stubStore) GetLayout(string) (*model.Layout, error)             { return nil, nil }
func (stubStore) SaveLayout(model.Layout) (bool, error)               { return false, nil }
func (stubStore) ResolveLayoutForScene(string) (*model.Layout, error) { return nil, nil }
func (stubStore) FirstLayoutID() (string, error)                      { return "", nil }
func (stubStore) DefaultLayoutID() (string, error)                    { return "", nil }
func (stubStore) RenameScene(string, string) (bool, error)            { return false, nil }

type stubObs struct{}

func (stubObs) Connected() bool   { return false }
func (stubObs) Scenes() []string  { return nil }
func (stubObs) LastError() string { return "" }
func (stubObs) RefreshScenes()    {}

type stubTW struct{}

func (stubTW) Token(context.Context, string) (string, string, error) { return "tok-bot", "botty", nil }
func (stubTW) Accounts() ([]twitch.Account, error) {
	return []twitch.Account{{Role: twitch.RoleStreamer, Login: "streamer1", UserID: "1"}, {Role: twitch.RoleBot, Login: "botty", UserID: "2"}}, nil
}

type ircConn struct {
	mu     sync.Mutex
	lines  []string
	in     chan string
	closed chan struct{}
	once   sync.Once
}

func (c *ircConn) ReadMessage() ([]byte, error) {
	select {
	case s := <-c.in:
		return []byte(s), nil
	case <-c.closed:
		return nil, io.EOF
	}
}
func (c *ircConn) WriteMessage(b []byte) error {
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\r\n") {
		c.mu.Lock()
		c.lines = append(c.lines, l)
		c.mu.Unlock()
		if strings.HasPrefix(l, "NICK ") {
			c.in <- ":tmi.twitch.tv 001 x :Welcome\r\n"
		}
	}
	return nil
}
func (c *ircConn) Close() error { c.once.Do(func() { close(c.closed) }); return nil }
func (c *ircConn) has(l string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, x := range c.lines {
		if x == l {
			return true
		}
	}
	return false
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for i := 0; i < 400; i++ {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timeout: " + what)
}

// Полный путь: клиент /ws → hub → chat.Service → «Twitch» и обратно.
func TestChatOverWebSocket(t *testing.T) {
	var mu sync.Mutex
	var conns []*ircConn
	svc := chat.NewWithDialer(stubTW{}, func(ctx context.Context) (chat.Conn, error) {
		c := &ircConn{in: make(chan string, 16), closed: make(chan struct{})}
		mu.Lock()
		conns = append(conns, c)
		mu.Unlock()
		return c, nil
	})
	defer svc.Close()
	byPass := func(pass string) *ircConn {
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			if c.has(pass) {
				return c
			}
		}
		return nil
	}

	am, _ := auth.New(&memAuthStore{set: map[string]string{}, devs: map[string]auth.Device{}})
	h := hub.New(stubStore{}, stubObs{}, func() string { return "" }, func() bool { return false })
	s := &Server{d: Deps{Auth: am, Hub: h, Chat: svc}}
	s.app = fiber.New()
	s.app.Use(s.originGuard)
	s.app.Use(s.authGuard)
	s.app.Get("/ws", fws.New(s.serveWS))
	s.chatWS()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go s.app.Listener(ln, fiber.ListenConfig{DisableStartupMessage: true})
	defer s.app.Shutdown()
	url := "ws://" + ln.Addr().String() + "/ws"

	// чужой origin (в том числе "null" у iframe в sandbox) не может открыть сокет
	for _, origin := range []string{"null", "https://evil.example"} {
		_, resp, err := websocket.DefaultDialer.Dial(url, http.Header{"Origin": {origin}})
		if err == nil || resp == nil || resp.StatusCode != 403 {
			t.Fatalf("origin %q must be rejected: err=%v resp=%v", origin, err, resp)
		}
	}
	ws, _, err := websocket.DefaultDialer.Dial(url, http.Header{"Origin": {"http://" + ln.Addr().String()}})
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	recv := func(typ string) map[string]any {
		t.Helper()
		_ = ws.SetReadDeadline(time.Now().Add(3 * time.Second))
		for {
			_, b, err := ws.ReadMessage()
			if err != nil {
				t.Fatalf("waiting for %s: %v", typ, err)
			}
			var m map[string]any
			_ = json.Unmarshal(b, &m)
			if m["type"] == typ {
				return m
			}
		}
	}
	send := func(v any) {
		if err := ws.WriteJSON(v); err != nil {
			t.Fatal(err)
		}
	}

	send(map[string]any{"type": "chat:sub", "reqId": 1, "channel": "SomeChan"})
	if r := recv("chat:sub:result"); r["ok"] != true || r["channel"] != "somechan" || r["reqId"] != float64(1) {
		t.Fatalf("sub result: %v", r)
	}
	send(map[string]any{"type": "chat:sub", "reqId": 2, "channel": "no way"})
	if r := recv("chat:sub:result"); r["ok"] != false || r["error"] == "" {
		t.Fatalf("bad sub result: %v", r)
	}
	var rd *ircConn
	eventually(t, "reader join", func() bool { rd = byPass("PASS SCHMOOPIIE"); return rd != nil && rd.has("JOIN #somechan") })

	rd.in <- "@id=m1;user-id=9;display-name=Viewer :viewer!viewer@viewer.tmi.twitch.tv PRIVMSG #somechan :!np\r\n"
	ev := recv("chat:event")
	for ev["event"] == "status" { // сообщение о состоянии соединения может прийти раньше
		ev = recv("chat:event")
	}
	msg, _ := ev["message"].(map[string]any)
	if ev["event"] != "message" || msg["text"] != "!np" || msg["displayName"] != "Viewer" {
		t.Fatalf("event: %v", ev)
	}

	send(map[string]any{"type": "chat:say", "reqId": 7, "channel": "somechan", "text": "hello", "replyTo": "m1"})
	if r := recv("chat:say:result"); r["ok"] != true || r["reqId"] != float64(7) {
		t.Fatalf("say result: %v", r)
	}
	if w := byPass("PASS oauth:tok-bot"); w == nil || !w.has("@reply-parent-msg-id=m1 PRIVMSG #somechan :hello") {
		t.Fatal("bot did not send the reply")
	}
	send(map[string]any{"type": "chat:say", "reqId": 8, "text": "   "})
	if r := recv("chat:say:result"); r["ok"] != false {
		t.Fatalf("empty say: %v", r)
	}

	// закрытие страницы снимает подписки: канал покидаем
	ws.Close()
	eventually(t, "PART after disconnect", func() bool { return rd.has("PART #somechan") })
}

func TestOriginGuard(t *testing.T) {
	s := &Server{}
	app := fiber.New()
	app.Use(s.originGuard)
	app.All("/x", func(c fiber.Ctx) error { return c.SendString("ok") })
	app.Get("/ws", func(c fiber.Ctx) error { return c.SendString("ok") })
	do := func(method, path, origin string) int {
		req := httptest.NewRequest(method, path, nil)
		req.Host = "127.0.0.1:8000"
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		resp, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode
	}
	cases := []struct {
		method, path, origin string
		want                 int
	}{
		{"POST", "/x", "", 200},                      // не браузер
		{"POST", "/x", "http://127.0.0.1:8000", 200}, // своя страница
		{"POST", "/x", "null", 403},                  // sandbox-iframe
		{"POST", "/x", "https://evil.example", 403},  // чужой сайт
		{"PUT", "/x", "http://localhost:8000", 403},  // другой хост — не наш origin
		{"GET", "/x", "https://evil.example", 200},   // чтение: ответ чужая страница не прочитает
		{"GET", "/ws", "null", 403},                  // но сокет — нельзя
		{"GET", "/ws", "http://127.0.0.1:8000", 200},
		{"GET", "/ws", "", 200},
	}
	for _, c := range cases {
		if got := do(c.method, c.path, c.origin); got != c.want {
			t.Errorf("%s %s Origin=%q: got %d want %d", c.method, c.path, c.origin, got, c.want)
		}
	}
}
