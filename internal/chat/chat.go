// Package chat — общий для всех плагинов доступ к чату Twitch. OC держит не более одного соединения на чтение
// (анонимное, без токена) и по соединению на каждый аккаунт, от имени которого пишут (бот / стример); плагины
// подписываются на каналы и отправляют сообщения через сервер, не открывая своих сокетов и не видя токенов.
package chat

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/fasthttp/websocket"

	"github.com/compico/overlaycomposer/internal/twitch"
)

const (
	ircURL      = "wss://irc-ws.chat.twitch.tv:443"
	maxTextLen  = 500
	readTimeout = 7 * time.Minute // Twitch шлёт PING примерно раз в 5 минут
	sayDedupTTL = 15 * time.Second
	botWindow   = 30 * time.Second
	botLimit    = 18 // у обычного аккаунта 20 сообщений за 30 с — оставляем запас
	ownerLimit  = 90 // у владельца канала и модераторов 100
)

var (
	chanRe    = regexp.MustCompile(`^[a-z0-9_]{1,25}$`)
	replyIDRe = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

	ErrBadChannel  = errors.New("invalid channel name")
	ErrNoChannel   = errors.New("no channel: pass one or connect the Streamer account")
	ErrBadText     = errors.New("message is empty or longer than 500 characters")
	ErrRateLimited = errors.New("rate limited: too many messages")
	ErrNoAccount   = errors.New("no Twitch account to send from: connect Bot or Streamer in Settings")
)

// Conn — одно текстовое WebSocket-соединение (в тестах подменяется).
type Conn interface {
	ReadMessage() ([]byte, error)
	WriteMessage(data []byte) error
	Close() error
}

// Dialer открывает соединение с IRC Twitch.
type Dialer func(ctx context.Context) (Conn, error)

// Twitch — то, что нужно от менеджера аккаунтов.
type Twitch interface {
	Token(ctx context.Context, role string) (token, login string, err error)
	Accounts() ([]twitch.Account, error)
}

// Event — то, что получает подписчик.
type Event struct {
	Name      string   `json:"event"` // message | delete | clear | status
	Channel   string   `json:"channel,omitempty"`
	Message   *Message `json:"message,omitempty"`
	TargetID  string   `json:"targetId,omitempty"` // delete: id удалённого сообщения
	Login     string   `json:"login,omitempty"`    // delete/clear: чьё сообщение/чьи сообщения
	Connected *bool    `json:"connected,omitempty"`
	Error     string   `json:"error,omitempty"`
}

type Deliver func(Event)

type subEntry struct {
	deliver Deliver
	n       int
}

type SayRequest struct {
	Channel string // "" — канал стримера
	Text    string
	As      string // "" (бот, иначе стример) | "bot" | "streamer"
	ReplyTo string // id сообщения, на которое отвечаем (Twitch покажет как ответ); защищает и от дублей
}

type Status struct {
	Connected bool     `json:"connected"`
	Channels  []string `json:"channels"`
	Error     string   `json:"error,omitempty"`
}

type Service struct {
	tw   Twitch
	dial Dialer
	now  func() time.Time

	ctx    context.Context
	cancel context.CancelFunc

	mu        sync.Mutex
	subs      map[string]map[any]*subEntry // канал → подписчик → запись
	rd        *client                      // текущее соединение на чтение (nil — нет)
	rdReady   bool
	started   bool
	lastError string
	botUserID string // кэш для FromBot

	wmu     sync.Mutex // сериализует открытие writer-ов и учёт лимитов
	writers map[string]*writer
	recent  map[string]time.Time // ключ дедупа ответов
}

func New(tw Twitch) *Service { return newService(tw, dialTwitch) }

// NewWithDialer — как New, но с подменой соединения (для тестов).
func NewWithDialer(tw Twitch, dial Dialer) *Service { return newService(tw, dial) }

func newService(tw Twitch, dial Dialer) *Service {
	ctx, cancel := context.WithCancel(context.Background())
	return &Service{
		tw: tw, dial: dial, now: time.Now, ctx: ctx, cancel: cancel,
		subs: map[string]map[any]*subEntry{}, writers: map[string]*writer{}, recent: map[string]time.Time{},
	}
}

// Close останавливает все соединения.
func (s *Service) Close() {
	s.cancel()
	s.mu.Lock()
	if s.rd != nil {
		_ = s.rd.close()
	}
	s.mu.Unlock()
	s.closeWriters()
}

// ---- подписки ----

// DefaultChannel — канал аккаунта стримера ("" — не подключён).
func (s *Service) DefaultChannel() string {
	accs, err := s.tw.Accounts()
	if err != nil {
		return ""
	}
	for _, a := range accs {
		if a.Role == twitch.RoleStreamer {
			return strings.ToLower(a.Login)
		}
	}
	return ""
}

func normalizeChannel(ch string) (string, bool) {
	ch = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(ch), "#"))
	return ch, chanRe.MatchString(ch)
}

// resolveChannel: "" → канал стримера.
func (s *Service) resolveChannel(ch string) (string, error) {
	if strings.TrimSpace(ch) == "" {
		if d := s.DefaultChannel(); d != "" {
			return d, nil
		}
		return "", ErrNoChannel
	}
	n, ok := normalizeChannel(ch)
	if !ok {
		return "", ErrBadChannel
	}
	return n, nil
}

// Subscribe подписывает key на канал и возвращает его настоящее имя. Подписки одного key на один канал считаются
// (каждому Subscribe должен соответствовать Unsubscribe или UnsubscribeAll).
func (s *Service) Subscribe(key any, channel string, d Deliver) (string, error) {
	ch, err := s.resolveChannel(channel)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	m := s.subs[ch]
	first := m == nil
	if first {
		m = map[any]*subEntry{}
		s.subs[ch] = m
	}
	if e := m[key]; e != nil {
		e.n++
		e.deliver = d
	} else {
		m[key] = &subEntry{deliver: d, n: 1}
	}
	if first && s.rd != nil && s.rdReady {
		_ = s.rd.send("JOIN #" + ch)
	}
	if !s.started {
		s.started = true
		go s.readerLoop()
	}
	s.mu.Unlock()
	return ch, nil
}

func (s *Service) Unsubscribe(key any, channel string) {
	ch, ok := normalizeChannel(channel)
	if !ok {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.unsubLocked(key, ch, false)
}

// UnsubscribeAll снимает все подписки key (соединение страницы закрылось).
func (s *Service) UnsubscribeAll(key any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for ch := range s.subs {
		s.unsubLocked(key, ch, true)
	}
}

func (s *Service) unsubLocked(key any, ch string, all bool) {
	m := s.subs[ch]
	e := m[key]
	if e == nil {
		return
	}
	if e.n--; e.n > 0 && !all {
		return
	}
	delete(m, key)
	if len(m) == 0 {
		delete(s.subs, ch)
		if s.rd != nil && s.rdReady {
			_ = s.rd.send("PART #" + ch)
		}
	}
}

func (s *Service) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	chs := make([]string, 0, len(s.subs))
	for c := range s.subs {
		chs = append(chs, c)
	}
	slices.Sort(chs)
	return Status{Connected: s.rd != nil && s.rdReady, Channels: chs, Error: s.lastError}
}

// emit рассылает событие подписчикам канала (вне блокировки: deliver может быть небыстрым).
func (s *Service) emit(ch string, ev Event) {
	s.mu.Lock()
	var ds []Deliver
	for _, e := range s.subs[ch] {
		ds = append(ds, e.deliver)
	}
	s.mu.Unlock()
	for _, d := range ds {
		d(ev)
	}
}

func (s *Service) emitAll(ev Event) {
	s.mu.Lock()
	seen := map[any]bool{}
	var ds []Deliver
	for _, m := range s.subs {
		for k, e := range m {
			if !seen[k] {
				seen[k] = true
				ds = append(ds, e.deliver)
			}
		}
	}
	s.mu.Unlock()
	for _, d := range ds {
		d(ev)
	}
}

// ---- чтение ----

func (s *Service) setStatus(connected bool, errMsg string) {
	s.mu.Lock()
	s.lastError = errMsg
	s.mu.Unlock()
	c := connected
	s.emitAll(Event{Name: "status", Connected: &c, Error: errMsg})
}

func (s *Service) readerLoop() {
	backoff := time.Second
	for s.ctx.Err() == nil {
		started := s.now()
		err := s.runReader()
		if s.ctx.Err() != nil {
			return
		}
		msg := "connection lost"
		if err != nil {
			msg = err.Error()
		}
		slog.Warn("chat: соединение на чтение потеряно", "err", msg)
		s.setStatus(false, msg)
		if s.now().Sub(started) > time.Minute {
			backoff = time.Second
		}
		wait := backoff/2 + time.Duration(rand.Int64N(int64(backoff)))
		select {
		case <-time.After(wait):
		case <-s.ctx.Done():
			return
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func (s *Service) runReader() error {
	conn, err := s.dial(s.ctx)
	if err != nil {
		return err
	}
	c := newClient(conn)
	defer c.close()
	nick := fmt.Sprintf("justinfan%d", 10000+rand.IntN(80000))
	for _, l := range []string{"CAP REQ :twitch.tv/tags twitch.tv/commands", "PASS SCHMOOPIIE", "NICK " + nick} {
		if err := c.send(l); err != nil {
			return err
		}
	}
	s.mu.Lock()
	s.rd, s.rdReady = c, false
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		if s.rd == c {
			s.rd, s.rdReady = nil, false
		}
		s.mu.Unlock()
	}()
	return c.readLoop(func(l ircLine) error { return s.onReaderLine(c, l) })
}

func (s *Service) onReaderLine(c *client, l ircLine) error {
	switch l.Cmd {
	case "PING":
		return c.send("PONG :" + strings.Join(l.Params, " "))
	case "RECONNECT":
		return errors.New("twitch asked to reconnect")
	case "001": // авторизация прошла (для анонимного входа тоже) — входим в каналы
		s.mu.Lock()
		s.rdReady = true
		chans := make([]string, 0, len(s.subs))
		for ch := range s.subs {
			chans = append(chans, "#"+ch)
		}
		s.mu.Unlock()
		slog.Info("chat: подключено к чату Twitch (чтение)", "channels", len(chans))
		s.setStatus(true, "")
		// JOIN пачками: у Twitch лимит на число входов в секунду
		for i := 0; i < len(chans); i += 10 {
			if err := c.send("JOIN " + strings.Join(chans[i:min(i+10, len(chans))], ",")); err != nil {
				return err
			}
		}
	case "NOTICE":
		slog.Debug("chat: notice", "params", l.Params)
	case "PRIVMSG":
		m, ok := messageFrom(l)
		if !ok {
			return nil
		}
		m.FromBot = s.isBot(m.UserID)
		s.emit(m.Channel, Event{Name: "message", Channel: m.Channel, Message: &m})
	case "CLEARMSG":
		if len(l.Params) > 0 && strings.HasPrefix(l.Params[0], "#") {
			ch := strings.ToLower(l.Params[0][1:])
			s.emit(ch, Event{Name: "delete", Channel: ch, TargetID: l.Tags["target-msg-id"], Login: strings.ToLower(l.Tags["login"])})
		}
	case "CLEARCHAT":
		if len(l.Params) > 0 && strings.HasPrefix(l.Params[0], "#") {
			ch := strings.ToLower(l.Params[0][1:])
			who := ""
			if len(l.Params) > 1 {
				who = strings.ToLower(l.Params[1])
			}
			s.emit(ch, Event{Name: "clear", Channel: ch, Login: who})
		}
	}
	return nil
}

func (s *Service) isBot(userID string) bool {
	if userID == "" {
		return false
	}
	accs, err := s.tw.Accounts()
	if err != nil {
		return false
	}
	for _, a := range accs {
		if a.Role == twitch.RoleBot && a.UserID == userID {
			return true
		}
	}
	return false
}

// ---- отправка ----

type writer struct {
	role   string
	login  string
	c      *client
	ready  chan struct{} // закрыт после 001
	err    error         // причина отказа (до ready)
	dead   chan struct{} // закрыт при разрыве
	joined map[string]bool
	sent   []time.Time
}

// Reload закрывает соединения для отправки (аккаунты или токены изменились): следующая отправка откроет новые.
func (s *Service) Reload() { s.closeWriters() }

func (s *Service) closeWriters() {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	for role, w := range s.writers {
		_ = w.c.close()
		delete(s.writers, role)
	}
}

// pickRole выбирает аккаунт для отправки.
func (s *Service) pickRole(as string) (string, error) {
	accs, err := s.tw.Accounts()
	if err != nil {
		return "", err
	}
	has := func(role string) bool {
		for _, a := range accs {
			if a.Role == role {
				return true
			}
		}
		return false
	}
	switch as {
	case "":
		for _, r := range []string{twitch.RoleBot, twitch.RoleStreamer} {
			if has(r) {
				return r, nil
			}
		}
	case twitch.RoleBot, twitch.RoleStreamer:
		if has(as) {
			return as, nil
		}
	default:
		return "", errors.New(`"as" must be "bot" or "streamer"`)
	}
	return "", ErrNoAccount
}

// Say отправляет сообщение от имени аккаунта. Возвращает nil, если сообщение ушло (или было отброшено как дубль ответа).
func (s *Service) Say(ctx context.Context, r SayRequest) error {
	ch, err := s.resolveChannel(r.Channel)
	if err != nil {
		return err
	}
	text := strings.Join(strings.Fields(strings.NewReplacer("\r", " ", "\n", " ").Replace(r.Text)), " ")
	if text == "" || utf8.RuneCountInString(text) > maxTextLen {
		return ErrBadText
	}
	if r.ReplyTo != "" && !replyIDRe.MatchString(r.ReplyTo) {
		return errors.New("invalid replyTo")
	}
	role, err := s.pickRole(r.As)
	if err != nil {
		return err
	}

	s.wmu.Lock()
	now := s.now()
	if r.ReplyTo != "" { // та же команда обработана на другой копии оверлея — второй ответ не нужен
		key := role + "|" + ch + "|" + r.ReplyTo + "|" + text
		for k, t := range s.recent {
			if now.Sub(t) > sayDedupTTL {
				delete(s.recent, k)
			}
		}
		if _, dup := s.recent[key]; dup {
			s.wmu.Unlock()
			return nil
		}
		s.recent[key] = now
	}
	s.wmu.Unlock()

	w, err := s.writerFor(ctx, role)
	if err != nil {
		return err
	}

	s.wmu.Lock()
	limit := botLimit
	if role == twitch.RoleStreamer {
		limit = ownerLimit
	}
	w.sent = slices.DeleteFunc(w.sent, func(t time.Time) bool { return now.Sub(t) > botWindow })
	if len(w.sent) >= limit {
		s.wmu.Unlock()
		return ErrRateLimited
	}
	w.sent = append(w.sent, now)
	needJoin := !w.joined[ch]
	w.joined[ch] = true
	s.wmu.Unlock()

	if needJoin {
		if err := w.c.send("JOIN #" + ch); err != nil {
			return err
		}
	}
	line := "PRIVMSG #" + ch + " :" + text
	if r.ReplyTo != "" {
		line = "@reply-parent-msg-id=" + r.ReplyTo + " " + line
	}
	return w.c.send(line)
}

func (s *Service) writerFor(ctx context.Context, role string) (*writer, error) {
	s.wmu.Lock()
	w := s.writers[role]
	s.wmu.Unlock()
	if w != nil {
		select {
		case <-w.dead:
		default:
			return w, w.waitReady(ctx)
		}
	}
	token, login, err := s.tw.Token(ctx, role)
	if err != nil {
		return nil, fmt.Errorf("twitch account %q: %w", role, err)
	}
	conn, err := s.dial(ctx)
	if err != nil {
		return nil, err
	}
	nw := &writer{role: role, login: login, c: newClient(conn), ready: make(chan struct{}), dead: make(chan struct{}), joined: map[string]bool{}}

	s.wmu.Lock()
	if cur := s.writers[role]; cur != nil { // пока подключались, кто-то успел раньше
		select {
		case <-cur.dead:
		default:
			s.wmu.Unlock()
			_ = nw.c.close()
			return cur, cur.waitReady(ctx)
		}
	}
	s.writers[role] = nw
	s.wmu.Unlock()

	go func() {
		err := nw.c.readLoop(func(l ircLine) error { return s.onWriterLine(nw, l) })
		if nw.err == nil {
			nw.err = err
		}
		close(nw.dead)
		s.wmu.Lock()
		if s.writers[role] == nw {
			delete(s.writers, role)
		}
		s.wmu.Unlock()
	}()
	for _, l := range []string{"PASS oauth:" + token, "NICK " + login} {
		if err := nw.c.send(l); err != nil {
			_ = nw.c.close()
			return nil, err
		}
	}
	return nw, nw.waitReady(ctx)
}

func (w *writer) waitReady(ctx context.Context) error {
	select {
	case <-w.ready:
		return nil
	case <-w.dead:
		if w.err != nil {
			return w.err
		}
		return errors.New("connection to Twitch closed")
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(15 * time.Second):
		return errors.New("timeout connecting to Twitch chat")
	}
}

func (s *Service) onWriterLine(w *writer, l ircLine) error {
	switch l.Cmd {
	case "PING":
		return w.c.send("PONG :" + strings.Join(l.Params, " "))
	case "RECONNECT":
		return errors.New("twitch asked to reconnect")
	case "001":
		select {
		case <-w.ready:
		default:
			close(w.ready)
			slog.Info("chat: подключено к чату Twitch (отправка)", "role", w.role, "login", w.login)
		}
	case "NOTICE":
		text := strings.Join(l.Params, " ")
		slog.Warn("chat: notice от Twitch", "role", w.role, "text", text)
		low := strings.ToLower(text)
		if strings.Contains(low, "authentication failed") || strings.Contains(low, "improperly formatted auth") {
			w.err = errors.New("Twitch rejected the token: reconnect the account in Settings")
			return w.err
		}
		s.mu.Lock()
		s.lastError = text
		s.mu.Unlock()
	}
	return nil
}

// ---- транспорт ----

// client — IRC-соединение: запись под блокировкой, чтение построчно.
type client struct {
	conn Conn
	wmu  sync.Mutex
}

func newClient(c Conn) *client { return &client{conn: c} }

func (c *client) send(line string) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return c.conn.WriteMessage([]byte(line + "\r\n"))
}

func (c *client) close() error { return c.conn.Close() }

func (c *client) readLoop(h func(ircLine) error) error {
	for {
		data, err := c.conn.ReadMessage()
		if err != nil {
			return err
		}
		for _, raw := range strings.Split(string(data), "\n") {
			if l, ok := parseLine(raw); ok {
				if err := h(l); err != nil {
					return err
				}
			}
		}
	}
}

type wsConn struct{ c *websocket.Conn }

func (w wsConn) ReadMessage() ([]byte, error) {
	_ = w.c.SetReadDeadline(time.Now().Add(readTimeout))
	_, b, err := w.c.ReadMessage()
	return b, err
}
func (w wsConn) WriteMessage(b []byte) error {
	_ = w.c.SetWriteDeadline(time.Now().Add(15 * time.Second))
	return w.c.WriteMessage(websocket.TextMessage, b)
}
func (w wsConn) Close() error { return w.c.Close() }

func dialTwitch(ctx context.Context) (Conn, error) {
	d := websocket.Dialer{HandshakeTimeout: 15 * time.Second, Proxy: http.ProxyFromEnvironment}
	c, _, err := d.DialContext(ctx, ircURL, nil)
	if err != nil {
		return nil, err
	}
	return wsConn{c}, nil
}
