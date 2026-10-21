package chat

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/compico/overlaycomposer/internal/twitch"
)

// ---- разбор ----

func TestParseLine(t *testing.T) {
	l, ok := parseLine("@badge-info=;badges=moderator/1,subscriber/12;color=#FF0000;display-name=Some\\sOne;emotes=25:0-4,12-16/1902:6-10;id=abc-1;mod=1;room-id=1;tmi-sent-ts=1700000000123;user-id=77 :someone!someone@someone.tmi.twitch.tv PRIVMSG #Chan :Kappa hi Kappa :)\r\n")
	if !ok || l.Cmd != "PRIVMSG" || l.Prefix != "someone!someone@someone.tmi.twitch.tv" || len(l.Params) != 2 || l.Params[0] != "#Chan" || l.Params[1] != "Kappa hi Kappa :)" {
		t.Fatalf("parse: %+v", l)
	}
	if l.Tags["display-name"] != "Some One" {
		t.Fatalf("tag unescape: %q", l.Tags["display-name"])
	}
	m, ok := messageFrom(l)
	if !ok || m.Channel != "chan" || m.Login != "someone" || m.DisplayName != "Some One" || m.UserID != "77" || m.ID != "abc-1" ||
		!m.IsMod || !m.IsSub || m.IsBroadcaster || m.Timestamp != 1700000000123 || m.Color != "#FF0000" || m.Action {
		t.Fatalf("message: %+v", m)
	}
	if len(m.Emotes) != 3 || m.Emotes[0] != (Emote{"25", 0, 4}) || m.Emotes[2] != (Emote{"1902", 6, 10}) {
		t.Fatalf("emotes: %+v", m.Emotes)
	}
	if len(m.Badges) != 2 || m.Badges[0] != "moderator" {
		t.Fatalf("badges: %v", m.Badges)
	}

	l, _ = parseLine(":u!u@u PRIVMSG #c :\x01ACTION waves\x01")
	if m, _ := messageFrom(l); !m.Action || m.Text != "waves" || m.DisplayName != "u" {
		t.Fatalf("action: %+v", m)
	}
	if l, ok := parseLine("PING :tmi.twitch.tv"); !ok || l.Cmd != "PING" || l.Params[0] != "tmi.twitch.tv" {
		t.Fatalf("ping: %+v", l)
	}
	if _, ok := parseLine(""); ok {
		t.Fatal("empty line parsed")
	}
	if _, ok := messageFrom(ircLine{Cmd: "PRIVMSG", Params: []string{"user", "x"}}); ok {
		t.Fatal("whisper-like treated as chat message")
	}
}

// ---- фейковый Twitch ----

type fakeConn struct {
	mu      sync.Mutex
	lines   []string
	in      chan string
	closed  chan struct{}
	once    sync.Once
	onWrite func(*fakeConn, string)
}

func (f *fakeConn) ReadMessage() ([]byte, error) {
	select {
	case s := <-f.in:
		return []byte(s), nil
	case <-f.closed:
		return nil, io.EOF
	}
}
func (f *fakeConn) WriteMessage(b []byte) error {
	select {
	case <-f.closed:
		return io.ErrClosedPipe
	default:
	}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\r\n") {
		f.mu.Lock()
		f.lines = append(f.lines, line)
		f.mu.Unlock()
		if f.onWrite != nil {
			f.onWrite(f, line)
		}
	}
	return nil
}
func (f *fakeConn) Close() error  { f.once.Do(func() { close(f.closed) }); return nil }
func (f *fakeConn) push(s string) { f.in <- s }
func (f *fakeConn) has(line string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, l := range f.lines {
		if l == line {
			return true
		}
	}
	return false
}
func (f *fakeConn) count(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, l := range f.lines {
		if strings.HasPrefix(l, prefix) {
			n++
		}
	}
	return n
}

type fakeNet struct {
	mu    sync.Mutex
	conns []*fakeConn
	// badAuth — PASS с этим токеном получает отказ
	badToken string
}

func (n *fakeNet) dial(ctx context.Context) (Conn, error) {
	fc := &fakeConn{in: make(chan string, 64), closed: make(chan struct{})}
	fc.onWrite = func(c *fakeConn, line string) {
		if strings.HasPrefix(line, "NICK ") {
			c.mu.Lock()
			bad := false
			for _, l := range c.lines {
				if n.badToken != "" && l == "PASS oauth:"+n.badToken {
					bad = true
				}
			}
			c.mu.Unlock()
			if bad {
				c.push(":tmi.twitch.tv NOTICE * :Login authentication failed\r\n")
				return
			}
			c.push(":tmi.twitch.tv 001 " + strings.TrimPrefix(line, "NICK ") + " :Welcome, GLHF!\r\n")
		}
	}
	n.mu.Lock()
	n.conns = append(n.conns, fc)
	n.mu.Unlock()
	return fc, nil
}

// find возвращает соединение, в котором есть строка (по PASS отличаем читателя от писателей).
func (n *fakeNet) find(t *testing.T, line string) *fakeConn {
	t.Helper()
	var got *fakeConn
	waitFor(t, func() bool {
		n.mu.Lock()
		defer n.mu.Unlock()
		for _, c := range n.conns {
			if c.has(line) {
				got = c
				return true
			}
		}
		return false
	})
	return got
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for i := 0; i < 400; i++ {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timeout waiting for condition")
}

type fakeTW struct {
	accs   []twitch.Account
	tokens map[string]string
}

func (f *fakeTW) Token(ctx context.Context, role string) (string, string, error) {
	for _, a := range f.accs {
		if a.Role == role {
			return f.tokens[role], a.Login, nil
		}
	}
	return "", "", twitch.ErrNoAccount
}
func (f *fakeTW) Accounts() ([]twitch.Account, error) { return f.accs, nil }

func newTestService(t *testing.T) (*Service, *fakeNet, *fakeTW) {
	fn := &fakeNet{}
	tw := &fakeTW{
		accs: []twitch.Account{
			{Role: twitch.RoleStreamer, Login: "Streamer1", UserID: "1"},
			{Role: twitch.RoleBot, Login: "botty", UserID: "2"},
		},
		tokens: map[string]string{"streamer": "tok-s", "bot": "tok-b"},
	}
	s := newService(tw, fn.dial)
	t.Cleanup(s.Close)
	return s, fn, tw
}

type collector struct {
	mu sync.Mutex
	ev []Event
}

func (c *collector) deliver(e Event) { c.mu.Lock(); c.ev = append(c.ev, e); c.mu.Unlock() }
func (c *collector) messages() []Message {
	c.mu.Lock()
	defer c.mu.Unlock()
	var o []Message
	for _, e := range c.ev {
		if e.Name == "message" {
			o = append(o, *e.Message)
		}
	}
	return o
}
func (c *collector) names() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var o []string
	for _, e := range c.ev {
		o = append(o, e.Name)
	}
	return o
}

// ---- чтение ----

func TestReaderSubscribeAndEvents(t *testing.T) {
	s, fn, _ := newTestService(t)
	a, b := &collector{}, &collector{}
	ch, err := s.Subscribe("A", "#SomeChan", a.deliver)
	if err != nil || ch != "somechan" {
		t.Fatalf("subscribe: %q %v", ch, err)
	}
	rd := fn.find(t, "PASS SCHMOOPIIE") // читатель анонимный: токен не нужен
	waitFor(t, func() bool { return rd.has("JOIN #somechan") })
	if !rd.has("CAP REQ :twitch.tv/tags twitch.tv/commands") {
		t.Fatal("no CAP REQ")
	}
	// второй подписчик того же канала — без повторного JOIN
	if _, err := s.Subscribe("B", "somechan", b.deliver); err != nil {
		t.Fatal(err)
	}
	// другой канал — отдельный JOIN
	if _, err := s.Subscribe("B", "other", b.deliver); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return rd.has("JOIN #other") })
	if rd.count("JOIN #somechan") != 1 {
		t.Fatalf("duplicate JOIN: %d", rd.count("JOIN #somechan"))
	}

	rd.push("PING :tmi.twitch.tv\r\n")
	waitFor(t, func() bool { return rd.has("PONG :tmi.twitch.tv") })

	rd.push("@id=m1;user-id=2;display-name=Botty :botty!botty@botty.tmi.twitch.tv PRIVMSG #somechan :hello\r\n" +
		"@id=m2;user-id=9 :viewer!viewer@viewer.tmi.twitch.tv PRIVMSG #other :other chat\r\n" +
		"@target-msg-id=m1;login=botty :tmi.twitch.tv CLEARMSG #somechan :hello\r\n" +
		":tmi.twitch.tv CLEARCHAT #somechan\r\n")
	waitFor(t, func() bool { return len(a.names()) >= 3 })
	ms := a.messages()
	if len(ms) != 1 || ms[0].Text != "hello" || !ms[0].FromBot {
		t.Fatalf("A messages: %+v", ms)
	}
	if got := strings.Join(a.names(), ","); !strings.Contains(got, "message,delete,clear") {
		t.Fatalf("A events: %s", got)
	}
	if len(b.messages()) != 2 {
		t.Fatalf("B should see both channels: %d", len(b.messages()))
	}

	// отписка: канал, где остался B, не покидаем; канал без подписчиков — PART
	s.Unsubscribe("A", "somechan")
	if rd.has("PART #somechan") {
		t.Fatal("PART although B still subscribed")
	}
	s.UnsubscribeAll("B")
	waitFor(t, func() bool { return rd.has("PART #somechan") && rd.has("PART #other") })
	if st := s.Status(); !st.Connected || len(st.Channels) != 0 {
		t.Fatalf("status: %+v", st)
	}
}

func TestSubscribeRefcountAndDefaultChannel(t *testing.T) {
	s, fn, _ := newTestService(t)
	c := &collector{}
	ch, err := s.Subscribe("A", "", c.deliver) // канал стримера
	if err != nil || ch != "streamer1" {
		t.Fatalf("default channel: %q %v", ch, err)
	}
	_, _ = s.Subscribe("A", "streamer1", c.deliver)
	rd := fn.find(t, "PASS SCHMOOPIIE")
	waitFor(t, func() bool { return rd.has("JOIN #streamer1") })
	s.Unsubscribe("A", "streamer1") // подписок было две
	if rd.has("PART #streamer1") {
		t.Fatal("left channel with one subscription remaining")
	}
	s.Unsubscribe("A", "streamer1")
	waitFor(t, func() bool { return rd.has("PART #streamer1") })

	if _, err := s.Subscribe("A", "bad channel!", c.deliver); err != ErrBadChannel {
		t.Fatalf("bad channel: %v", err)
	}
	s2 := newService(&fakeTW{}, fn.dial)
	defer s2.Close()
	if _, err := s2.Subscribe("A", "", c.deliver); err != ErrNoChannel {
		t.Fatalf("no default: %v", err)
	}
}

func TestReaderReconnectRejoins(t *testing.T) {
	s, fn, _ := newTestService(t)
	c := &collector{}
	_, _ = s.Subscribe("A", "chan1", c.deliver)
	rd := fn.find(t, "PASS SCHMOOPIIE")
	waitFor(t, func() bool { return rd.has("JOIN #chan1") })
	rd.push(":tmi.twitch.tv RECONNECT\r\n")
	// после разрыва читатель переподключается (с задержкой) и входит в канал заново
	waitFor(t, func() bool {
		fn.mu.Lock()
		defer fn.mu.Unlock()
		for _, x := range fn.conns {
			if x != rd && x.has("JOIN #chan1") {
				return true
			}
		}
		return false
	})
}

// ---- отправка ----

func TestSay(t *testing.T) {
	s, fn, _ := newTestService(t)
	ctx := context.Background()
	if err := s.Say(ctx, SayRequest{Channel: "streamer1", Text: "hi there", As: "bot"}); err != nil {
		t.Fatal(err)
	}
	w := fn.find(t, "PASS oauth:tok-b")
	if !w.has("NICK botty") || !w.has("JOIN #streamer1") || !w.has("PRIVMSG #streamer1 :hi there") {
		t.Fatalf("writer lines: %v", w.lines)
	}
	// канал по умолчанию, аккаунт по умолчанию (бот), ответ на сообщение, переводы строк схлопываются
	if err := s.Say(ctx, SayRequest{Text: "line1\nline2", ReplyTo: "abc-123"}); err != nil {
		t.Fatal(err)
	}
	if !w.has("@reply-parent-msg-id=abc-123 PRIVMSG #streamer1 :line1 line2") {
		t.Fatalf("reply line missing: %v", w.lines)
	}
	if w.count("JOIN #streamer1") != 1 || w.count("PASS") != 1 {
		t.Fatal("writer must be reused and join once")
	}
	// тот же ответ на то же сообщение (вторая копия оверлея) — молча отбрасывается
	if err := s.Say(ctx, SayRequest{Text: "line1\nline2", ReplyTo: "abc-123"}); err != nil || w.count("PRIVMSG #streamer1 :line1 line2")+w.count("@reply-parent-msg-id=abc-123 PRIVMSG") != 1 {
		t.Fatalf("dedup: %v", err)
	}
	// от имени стримера — другое соединение со своим токеном
	if err := s.Say(ctx, SayRequest{Text: "from owner", As: "streamer"}); err != nil {
		t.Fatal(err)
	}
	sw := fn.find(t, "PASS oauth:tok-s")
	if !sw.has("NICK Streamer1") || !sw.has("PRIVMSG #streamer1 :from owner") {
		t.Fatalf("streamer writer: %v", sw.lines)
	}
	// валидация
	for _, bad := range []SayRequest{{Text: "  "}, {Text: strings.Repeat("я", 501)}} {
		if err := s.Say(ctx, bad); err != ErrBadText {
			t.Fatalf("bad text %q: %v", bad.Text[:1], err)
		}
	}
	if err := s.Say(ctx, SayRequest{Text: "x", Channel: "no way"}); err != ErrBadChannel {
		t.Fatalf("bad channel: %v", err)
	}
	if err := s.Say(ctx, SayRequest{Text: "x", As: "admin"}); err == nil {
		t.Fatal("unknown role accepted")
	}
	if err := s.Say(ctx, SayRequest{Text: "x", ReplyTo: "bad id with spaces"}); err == nil {
		t.Fatal("bad replyTo accepted")
	}
}

func TestSayFallbackAndNoAccount(t *testing.T) {
	s, fn, tw := newTestService(t)
	tw.accs = tw.accs[:1] // остался только стример: «auto» пишет от него, явный bot — ошибка
	if err := s.Say(context.Background(), SayRequest{Channel: "c", Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	fn.find(t, "PRIVMSG #c :hi")
	if err := s.Say(context.Background(), SayRequest{Channel: "c", Text: "hi", As: "bot"}); err != ErrNoAccount {
		t.Fatalf("explicit bot: %v", err)
	}
	tw.accs = nil
	if err := s.Say(context.Background(), SayRequest{Channel: "c", Text: "hi"}); err != ErrNoAccount {
		t.Fatalf("no accounts: %v", err)
	}
}

func TestSayRateLimit(t *testing.T) {
	s, _, _ := newTestService(t)
	for i := 0; i < botLimit; i++ {
		if err := s.Say(context.Background(), SayRequest{Channel: "c", Text: "m", As: "bot"}); err != nil {
			t.Fatalf("msg %d: %v", i, err)
		}
	}
	if err := s.Say(context.Background(), SayRequest{Channel: "c", Text: "m", As: "bot"}); err != ErrRateLimited {
		t.Fatalf("limit: %v", err)
	}
	// другой аккаунт считается отдельно
	if err := s.Say(context.Background(), SayRequest{Channel: "c", Text: "m", As: "streamer"}); err != nil {
		t.Fatalf("streamer: %v", err)
	}
}

func TestSayRejectedToken(t *testing.T) {
	s, fn, _ := newTestService(t)
	fn.badToken = "tok-b"
	err := s.Say(context.Background(), SayRequest{Channel: "c", Text: "hi", As: "bot"})
	if err == nil || !strings.Contains(err.Error(), "reconnect the account") {
		t.Fatalf("rejected token: %v", err)
	}
}

func TestReloadClosesWriters(t *testing.T) {
	s, fn, _ := newTestService(t)
	if err := s.Say(context.Background(), SayRequest{Channel: "c", Text: "one", As: "bot"}); err != nil {
		t.Fatal(err)
	}
	w := fn.find(t, "PASS oauth:tok-b")
	s.Reload()
	waitFor(t, func() bool {
		select {
		case <-w.closed:
			return true
		default:
			return false
		}
	})
	if err := s.Say(context.Background(), SayRequest{Channel: "c", Text: "two", As: "bot"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		fn.mu.Lock()
		defer fn.mu.Unlock()
		n := 0
		for _, c := range fn.conns {
			if c.has("PASS oauth:tok-b") {
				n++
			}
		}
		return n == 2
	})
}
