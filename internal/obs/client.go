// Package obs — клиент obs-websocket 5.x (подпротокол obswebsocket.json).
// Рукопожатие: Hello(0) → Identify(1) → Identified(2); события — op 5; запросы/ответы — op 6/7.
package obs

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fasthttp/websocket"
)

const subprotocol = "obswebsocket.json"

// подписка на события категории Scenes (1<<2): смена сцены, список, создание/удаление/переименование
const eventSubscriptions = 1 << 2

type Client struct {
	mu        sync.Mutex
	url       string
	password  string
	cancel    context.CancelFunc
	running   bool
	connected bool
	scene     string
	scenes    []string
	lastErr   string
	loggedErr string
	conn      *websocket.Conn
	reqSeq    int
	pending   map[string]chan map[string]any

	writeMu sync.Mutex // запись в сокет — только по одному

	onScene   func(string)
	onStatus  func(bool)
	onScenes  func([]string)
	onRenamed func(from, to string)
}

func New(url, password string) *Client {
	return &Client{url: url, password: password, pending: map[string]chan map[string]any{}}
}

// Обработчики вызываются из горутин клиента без удержания его блокировок.
func (c *Client) OnScene(f func(string))                 { c.onScene = f }
func (c *Client) OnStatus(f func(bool))                  { c.onStatus = f }
func (c *Client) OnSceneList(f func([]string))           { c.onScenes = f }
func (c *Client) OnSceneRenamed(f func(from, to string)) { c.onRenamed = f }

func (c *Client) Connected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.connected
}

func (c *Client) CurrentScene() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.scene
}

func (c *Client) Scenes() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.scenes...)
}

// LastError — почему сейчас нет соединения ("" — подключено или ещё не пробовали).
func (c *Client) LastError() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastErr
}

// Start запускает подключение (с автопереподключением), если оно ещё не запущено.
func (c *Client) Start() {
	c.mu.Lock()
	if c.running {
		c.mu.Unlock()
		return
	}
	c.startLocked()
	c.mu.Unlock()
}

func (c *Client) startLocked() {
	ctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel
	c.running = true
	go c.loop(ctx, c.url, c.password)
}

// Stop отключается и больше не переподключается (до Start/Reconnect).
func (c *Client) Stop() {
	c.mu.Lock()
	c.stopLocked()
	c.mu.Unlock()
	c.fireStatus(false)
}

func (c *Client) stopLocked() {
	if c.cancel != nil {
		c.cancel()
		c.cancel = nil
	}
	c.running = false
	if c.conn != nil {
		_ = c.conn.Close()
		c.conn = nil
	}
	c.failPendingLocked()
	c.connected = false
	c.scene = ""
	c.lastErr = ""
	c.loggedErr = ""
}

// Reconnect: новые параметры или ручной «Reconnect» — рвём соединение и подключаемся заново сразу.
func (c *Client) Reconnect(url, password string) {
	c.mu.Lock()
	wasConnected := c.connected
	c.stopLocked()
	c.url, c.password = url, password
	c.scenes = nil
	c.startLocked()
	c.mu.Unlock()
	if wasConnected {
		c.fireStatus(false)
	}
}

func (c *Client) failPendingLocked() {
	for id, ch := range c.pending {
		close(ch)
		delete(c.pending, id)
	}
}

func (c *Client) fireStatus(v bool) {
	if c.onStatus != nil {
		c.onStatus(v)
	}
}

func (c *Client) setConnected(v bool) {
	c.mu.Lock()
	changed := c.connected != v
	c.connected = v
	c.mu.Unlock()
	if changed {
		c.fireStatus(v)
	}
}

func (c *Client) fail(msg string) {
	c.mu.Lock()
	c.lastErr = msg
	log := msg != c.loggedErr // не спамим одной и той же ошибкой при каждой попытке
	c.loggedErr = msg
	c.mu.Unlock()
	if log {
		slog.Warn("obs: " + msg)
	}
}

func (c *Client) loop(ctx context.Context, url, password string) {
	retry := time.Second
	for ctx.Err() == nil {
		identified, err := c.session(ctx, url, password)
		c.mu.Lock()
		if c.conn != nil && ctx.Err() == nil {
			c.conn = nil
		}
		c.failPendingLocked()
		c.mu.Unlock()
		if ctx.Err() != nil {
			return
		}
		c.setConnected(false)
		c.fail(err.Error())
		if identified {
			retry = time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(retry):
		}
		if retry *= 2; retry > 10*time.Second {
			retry = 10 * time.Second
		}
	}
}

type message struct {
	Op int            `json:"op"`
	D  map[string]any `json:"d"`
}

// session — одно соединение; возвращает, дошли ли до Identified, и причину завершения (человекочитаемую).
func (c *Client) session(ctx context.Context, url, password string) (identified bool, _ error) {
	d := websocket.Dialer{Subprotocols: []string{subprotocol}, HandshakeTimeout: 5 * time.Second}
	conn, _, err := d.DialContext(ctx, url, http.Header{})
	if err != nil {
		return false, errors.New(explain(url, false, 0, "", err.Error()))
	}
	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()
	defer conn.Close()
	go func() { // ctx отменён (Stop/Reconnect) — разблокировать ReadMessage
		<-ctx.Done()
		_ = conn.Close()
	}()

	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			if ce, ok := errors.AsType[*websocket.CloseError](err); ok {
				return identified, errors.New(explain(url, true, ce.Code, ce.Text, ""))
			}
			return identified, errors.New(explain(url, true, 0, "", err.Error()))
		}
		var m message
		if json.Unmarshal(raw, &m) != nil {
			continue
		}
		switch m.Op {
		case 0: // Hello
			id := map[string]any{"rpcVersion": 1, "eventSubscriptions": eventSubscriptions}
			if auth, ok := m.D["authentication"].(map[string]any); ok {
				challenge, _ := auth["challenge"].(string)
				salt, _ := auth["salt"].(string)
				id["authentication"] = sha256b64(sha256b64(password+salt) + challenge)
			}
			if err := c.write(conn, map[string]any{"op": 1, "d": id}); err != nil {
				return false, err
			}
		case 2: // Identified
			identified = true
			c.mu.Lock()
			c.lastErr, c.loggedErr = "", ""
			c.mu.Unlock()
			c.setConnected(true)
			slog.Info("obs: connected")
			go c.afterIdentified()
		case 5: // событие
			c.handleEvent(m.D)
		case 7: // ответ на запрос
			c.deliver(m.D)
		}
	}
}

func (c *Client) afterIdentified() {
	c.RefreshScenes()
	if r, ok := c.Request("GetCurrentProgramScene", nil); ok {
		name, _ := r["sceneName"].(string)
		if name == "" {
			name, _ = r["currentProgramSceneName"].(string)
		}
		if name != "" {
			c.setScene(name)
		}
	}
}

func (c *Client) handleEvent(d map[string]any) {
	typ, _ := d["eventType"].(string)
	data, _ := d["eventData"].(map[string]any)
	switch typ {
	case "CurrentProgramSceneChanged":
		if n, _ := data["sceneName"].(string); n != "" {
			c.setScene(n)
		}
	case "SceneNameChanged":
		from, _ := data["oldSceneName"].(string)
		to, _ := data["sceneName"].(string)
		if from == "" || to == "" {
			return
		}
		slog.Info("obs: сцена переименована", "from", from, "to", to)
		c.mu.Lock()
		if c.scene == from {
			c.scene = to
		}
		c.mu.Unlock()
		if c.onRenamed != nil {
			c.onRenamed(from, to)
		}
		go c.RefreshScenes()
	case "SceneListChanged", "SceneCreated", "SceneRemoved":
		go c.RefreshScenes()
	}
}

func (c *Client) setScene(name string) {
	c.mu.Lock()
	c.scene = name
	c.mu.Unlock()
	slog.Info("obs: scene", "name", name)
	if c.onScene != nil {
		c.onScene(name)
	}
}

func (c *Client) write(conn *websocket.Conn, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return conn.WriteMessage(websocket.TextMessage, b)
}

func (c *Client) deliver(d map[string]any) {
	id, _ := d["requestId"].(string)
	c.mu.Lock()
	ch, ok := c.pending[id]
	delete(c.pending, id)
	c.mu.Unlock()
	if !ok {
		return
	}
	st, _ := d["requestStatus"].(map[string]any)
	if okRes, _ := st["result"].(bool); okRes {
		resp, _ := d["responseData"].(map[string]any)
		if resp == nil {
			resp = map[string]any{}
		}
		ch <- resp
	}
	close(ch)
}

// Request отправляет запрос и ждёт ответ (до 5 с). false — нет соединения, ошибка или таймаут.
func (c *Client) Request(typ string, data map[string]any) (map[string]any, bool) {
	c.mu.Lock()
	conn := c.conn
	if conn == nil {
		c.mu.Unlock()
		return nil, false
	}
	c.reqSeq++
	id := strconv.Itoa(c.reqSeq)
	ch := make(chan map[string]any, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	d := map[string]any{"requestType": typ, "requestId": id}
	if data != nil {
		d["requestData"] = data
	}
	if err := c.write(conn, map[string]any{"op": 6, "d": d}); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, false
	}
	select {
	case r, ok := <-ch:
		return r, ok && r != nil
	case <-time.After(5 * time.Second):
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, false
	}
}

// ListScenes запрашивает у OBS список сцен (в порядке, который отдаёт OBS).
func (c *Client) ListScenes() []string {
	r, ok := c.Request("GetSceneList", nil)
	if !ok {
		return nil
	}
	arr, _ := r["scenes"].([]any)
	out := make([]string, 0, len(arr))
	for _, it := range arr {
		if m, ok := it.(map[string]any); ok {
			if n, _ := m["sceneName"].(string); n != "" {
				out = append(out, n)
			}
		}
	}
	return out
}

// RefreshScenes обновляет кэш списка сцен и уведомляет, если он изменился.
func (c *Client) RefreshScenes() {
	list := c.ListScenes()
	if len(list) == 0 {
		return
	}
	c.mu.Lock()
	same := strings.Join(list, "\x00") == strings.Join(c.scenes, "\x00")
	if !same {
		c.scenes = list
	}
	c.mu.Unlock()
	if !same && c.onScenes != nil {
		c.onScenes(list)
	}
}

func sha256b64(s string) string {
	h := sha256.Sum256([]byte(s))
	return base64.StdEncoding.EncodeToString(h[:])
}

var hostPort = regexp.MustCompile(`^\[?([^\]:]+)`)

// wslHint: внутри WSL 127.0.0.1 — это Linux, а не Windows, где запущен OBS.
func wslHint(rawURL string) string {
	ver, err := os.ReadFile("/proc/version")
	if err != nil {
		return ""
	}
	if s := strings.ToLower(string(ver)); !strings.Contains(s, "microsoft") && !strings.Contains(s, "wsl") {
		return ""
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	if h := u.Hostname(); h != "127.0.0.1" && h != "localhost" && h != "::1" {
		return ""
	}
	ip := ""
	if rc, err := os.ReadFile("/etc/resolv.conf"); err == nil {
		for _, line := range strings.Split(string(rc), "\n") {
			if f := strings.Fields(line); len(f) == 2 && f[0] == "nameserver" {
				ip = f[1]
				break
			}
		}
	}
	hint := " Сервер запущен в WSL: там 127.0.0.1 — это не Windows. Используйте адрес Windows-хоста"
	if ip != "" {
		hint += fmt.Sprintf(" (скорее всего ws://%s:4455)", ip)
	}
	return hint + ", разрешите порт в брандмауэре Windows или запустите Windows-версию приложения."
}

// explain — человеческое объяснение, почему соединения нет.
func explain(url string, opened bool, code int, reason, errMsg string) string {
	if !opened {
		s := "не удалось подключиться к " + url
		if errMsg != "" {
			s += " (" + errMsg + ")"
		}
		return s + ". Проверьте, что OBS запущен, включён Tools → WebSocket Server Settings → «Enable WebSocket server», " +
			"и совпадают адрес и порт." + wslHint(url)
	}
	switch code {
	case 4009:
		return "OBS отклонил пароль: введите пароль из Tools → WebSocket Server Settings (или отключите там аутентификацию)"
	case 4010:
		return "версия протокола не поддерживается: нужен obs-websocket 5.x (OBS 28+)"
	}
	s := fmt.Sprintf("OBS закрыл соединение (код %d", code)
	if reason != "" {
		s += ": " + reason
	}
	if code == 0 && errMsg != "" {
		s += ": " + errMsg
	}
	return s + ")"
}
