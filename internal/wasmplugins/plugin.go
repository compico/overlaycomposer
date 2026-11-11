package wasmplugins

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/compico/overlaycomposer/internal/events"
	"github.com/compico/overlaycomposer/internal/wasmassets"
)

// Состояния плагина.
const (
	StateRunning = "running"
	StateFailed  = "failed"  // слишком часто падал: ждёт ручной перезагрузки
	StateStopped = "stopped" // остановлен менеджером
)

const (
	callTimeout     = 10 * time.Second
	eventQueueSize  = 256
	maxTimers       = 16
	minTimerMS      = 50
	maxTimerMS      = 24 * 3600 * 1000
	maxKeyLen       = 128
	maxLogMsg       = 2000
	maxLogFields    = 16
	crashLimit      = 5 // столько падений за crashWindow — и плагин считается сломанным
	crashWindow     = time.Minute
	maxResponseBody = 1 << 20
)

// Ошибки Handle, по которым сервер выбирает код ответа.
var (
	ErrNoRoute     = errors.New("no such route")
	ErrUnavailable = errors.New("plugin is not running")
)

// Storage — постоянное хранилище плагина (в OC — kv.Service с пространством имён по id плагина).
type Storage interface {
	Get(ctx context.Context, plugin, key string) (json.RawMessage, bool, error)
	Set(ctx context.Context, plugin, key string, value json.RawMessage) error
	Delete(ctx context.Context, plugin, key string) error
}

// Services — то, чем хост обслуживает плагины. Любое поле может быть nil: соответствующие операции вернут ошибку.
type Services struct {
	Storage Storage
	// Redeem меняет статус погашения награды Twitch (FULFILLED/CANCELED).
	Redeem func(ctx context.Context, rewardID, redemptionID, status string) error
	// Fetch — клиент для http.fetch; nil — клиент, закрытый от локальной сети.
	Fetch *http.Client
	Log   *slog.Logger
	// Events — брокер событий для events.publish; nil — публикация недоступна.
	Events EventBus
}

// EventBus — то, что плагинам нужно от брокера событий (его реализует *events.Broker).
type EventBus interface {
	Declare(plugin string, topics []string) error
	Forget(plugin string)
	Publish(plugin, topic string, data json.RawMessage, retain bool) error
}

// Request/Response — обращение к маршруту плагина.
type Request struct {
	Method string            `json:"method"`
	Path   string            `json:"path"`
	Query  map[string]string `json:"query"`
	Body   json.RawMessage   `json:"body"`
}

type Response struct {
	Status int             `json:"status"`
	Body   json.RawMessage `json:"body"`
}

type event struct {
	Type string `json:"type"`
	Data any    `json:"data"`
}

// Info — снимок состояния плагина для интерфейса.
type Info struct {
	Meta      Meta            `json:"meta"`
	File      string          `json:"file"`
	SHA256    string          `json:"sha256"`
	State     string          `json:"state"`
	LastError string          `json:"lastError,omitempty"`
	Restarts  int             `json:"restarts"`
	Granted   []string        `json:"granted"`
	Config    json.RawMessage `json:"config,omitempty"`
}

// Plugin — один загруженный плагин: модуль, текущий экземпляр, очередь событий и таймеры.
// Экземпляр однопоточный: маршруты и события идут через мьютекс. Состояние плагина лежит в Storage хоста,
// поэтому перезапуск экземпляра после падения ничего не теряет.
type Plugin struct {
	meta Meta
	file string
	sha  string
	mod  Module
	svc  Services
	log  *slog.Logger

	assets *wasmassets.Assets // статика из секции oc.assets; nil — её нет

	ctx    context.Context
	cancel context.CancelFunc

	mu   sync.Mutex // сериализует работу с inst
	inst Instance

	stMu      sync.Mutex
	state     string
	lastErr   string
	restarts  int
	failTimes []time.Time
	cfg       json.RawMessage
	grants    map[string]bool

	events chan event
	done   chan struct{}

	tmMu   sync.Mutex
	timers map[string]context.CancelFunc

	infly chan struct{}
	http  *http.Client
}

func newPlugin(parent context.Context, svc Services, mod Module, meta Meta, file, sha string, cfg json.RawMessage, grants []string) *Plugin {
	ctx, cancel := context.WithCancel(parent)
	log := svc.Log
	if log == nil {
		log = slog.Default()
	}
	cl := svc.Fetch
	if cl == nil {
		cl = newFetchClient(false)
	}
	p := &Plugin{
		meta: meta, file: file, sha: sha, mod: mod, svc: svc, ctx: ctx, cancel: cancel,
		log:    log.With("plugin", meta.ID),
		state:  StateStopped,
		cfg:    normConfig(cfg),
		grants: map[string]bool{},
		events: make(chan event, eventQueueSize),
		done:   make(chan struct{}),
		timers: map[string]context.CancelFunc{},
		infly:  make(chan struct{}, fetchMaxInfly),
		http:   cl,
	}
	p.setGrants(grants)
	return p
}

func normConfig(c json.RawMessage) json.RawMessage {
	if len(bytes.TrimSpace(c)) == 0 {
		return json.RawMessage(`{}`)
	}
	return c
}

// Start запускает первый экземпляр и обработчик событий. Ошибка запуска не фатальна для менеджера: плагин будет в failed.
func (p *Plugin) Start() error {
	if p.svc.Events != nil {
		if err := p.svc.Events.Declare(p.meta.ID, p.meta.EventTopics()); err != nil {
			p.log.Warn("events: topics were not declared", "err", err)
		}
	}
	p.mu.Lock()
	err := p.startLocked()
	p.mu.Unlock()
	go p.worker()
	if err != nil {
		p.fail(err)
	}
	return err
}

func (p *Plugin) startLocked() error {
	inst, err := p.mod.New(p.ctx, p.hostCall, &lineWriter{log: p.log})
	if err != nil {
		return fmt.Errorf("instantiate: %w", err)
	}
	arg, _ := json.Marshal(map[string]any{"apiVersion": APIVersion, "config": json.RawMessage(p.config())})
	ctx, cancel := context.WithTimeout(p.ctx, callTimeout)
	defer cancel()
	if _, err := inst.Init(ctx, arg); err != nil {
		_ = inst.Close(context.Background())
		return fmt.Errorf("oc_init: %w", err)
	}
	p.inst = inst
	p.stMu.Lock()
	p.state, p.lastErr = StateRunning, ""
	p.stMu.Unlock()
	p.log.Info("plugin started", "version", p.meta.Version, "routes", len(p.meta.Routes))
	return nil
}

// Stop останавливает таймеры, очередь и экземпляр. Повторный вызов безвреден.
func (p *Plugin) Stop() {
	if p.svc.Events != nil {
		p.svc.Events.Forget(p.meta.ID)
	}
	p.cancel()
	p.tmMu.Lock()
	for id, c := range p.timers {
		c()
		delete(p.timers, id)
	}
	p.tmMu.Unlock()
	<-p.done // worker завершился: новых событий в экземпляр не будет
	p.mu.Lock()
	if p.inst != nil {
		_ = p.inst.Close(context.Background())
		p.inst = nil
	}
	p.mu.Unlock()
	p.stMu.Lock()
	p.state = StateStopped
	p.stMu.Unlock()
	_ = p.mod.Close(context.Background())
}

func (p *Plugin) fail(err error) {
	p.stMu.Lock()
	p.state, p.lastErr = StateFailed, err.Error()
	p.stMu.Unlock()
	p.log.Error("plugin failed", "err", err)
}

// call исполняет обработчик под мьютексом. Ошибка рантайма (trap, таймаут) — экземпляр считается испорченным:
// он пересоздаётся, а если это случается слишком часто, плагин переходит в failed.
func (p *Plugin) call(ctx context.Context, name string, arg []byte) ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.inst == nil {
		return nil, ErrUnavailable
	}
	cctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	out, err := p.inst.Call(cctx, name, arg)
	if err == nil {
		return out, nil
	}
	p.log.Error("call crashed", "handler", name, "err", err)
	p.restartLocked(err)
	return nil, fmt.Errorf("plugin crashed in %s: %w", name, err)
}

func (p *Plugin) restartLocked(cause error) {
	_ = p.inst.Close(context.Background())
	p.inst = nil
	now := time.Now()
	p.stMu.Lock()
	p.restarts++
	keep := p.failTimes[:0]
	for _, t := range p.failTimes {
		if now.Sub(t) < crashWindow {
			keep = append(keep, t)
		}
	}
	p.failTimes = append(keep, now)
	tooMany := len(p.failTimes) >= crashLimit
	p.stMu.Unlock()
	if tooMany {
		p.fail(fmt.Errorf("crashed %d times in %s, last: %w", crashLimit, crashWindow, cause))
		p.stopTimers()
		return
	}
	if err := p.startLocked(); err != nil {
		p.fail(err)
		p.stopTimers()
	}
}

func (p *Plugin) stopTimers() {
	p.tmMu.Lock()
	defer p.tmMu.Unlock()
	for id, c := range p.timers {
		c()
		delete(p.timers, id)
	}
}

// Handle вызывает обработчик маршрута. Ответ плагина — {status, body}; статус по умолчанию 200.
func (p *Plugin) Handle(ctx context.Context, req Request) (Response, error) {
	var handler string
	for _, r := range p.meta.Routes {
		if r.Method == req.Method && r.Path == req.Path {
			handler = r.Handler
			break
		}
	}
	if handler == "" {
		return Response{}, ErrNoRoute
	}
	if req.Query == nil {
		req.Query = map[string]string{}
	}
	if len(bytes.TrimSpace(req.Body)) == 0 {
		req.Body = json.RawMessage(`null`)
	}
	arg, err := json.Marshal(req)
	if err != nil {
		return Response{}, err
	}
	out, err := p.call(ctx, handler, arg)
	if err != nil {
		return Response{}, err
	}
	var res Response
	if len(out) == 0 {
		return Response{Status: http.StatusNoContent}, nil
	}
	if err := json.Unmarshal(out, &res); err != nil {
		return Response{}, fmt.Errorf("handler %s returned invalid response: %w", handler, err)
	}
	if res.Status == 0 {
		res.Status = http.StatusOK
	}
	if res.Status < 100 || res.Status > 599 {
		return Response{}, fmt.Errorf("handler %s returned status %d", handler, res.Status)
	}
	if len(res.Body) > maxResponseBody {
		return Response{}, fmt.Errorf("handler %s returned too large body", handler)
	}
	return res, nil
}

// Deliver ставит событие в очередь плагина. Если плагин не подписан или нет разрешения — событие не доставляется.
// Очередь ограничена: при переполнении событие отбрасывается (с записью в лог), а не растёт память.
func (p *Plugin) Deliver(typ string, data any) bool {
	if !p.meta.Subscribes(typ) && typ != EventTimer && typ != EventHTTPResponse {
		return false
	}
	if typ == EventRedemption && !p.granted(PermRedeem) {
		return false
	}
	return p.enqueue(typ, data)
}

func (p *Plugin) enqueue(typ string, data any) bool {
	select {
	case p.events <- event{Type: typ, Data: data}:
		return true
	default:
		p.log.Warn("event queue is full, event dropped", "type", typ)
		return false
	}
}

func (p *Plugin) worker() {
	defer close(p.done)
	for {
		select {
		case <-p.ctx.Done():
			return
		case ev := <-p.events:
			arg, err := json.Marshal(ev)
			if err != nil {
				p.log.Error("event encode", "type", ev.Type, "err", err)
				continue
			}
			if _, err := p.call(p.ctx, "on_event", arg); err != nil && !errors.Is(err, ErrUnavailable) {
				p.log.Warn("on_event failed", "type", ev.Type, "err", err)
			}
		}
	}
}

func (p *Plugin) granted(perm string) bool {
	p.stMu.Lock()
	defer p.stMu.Unlock()
	return p.grants[perm]
}

// setGrants оставляет только то, что плагин действительно запрашивал.
func (p *Plugin) setGrants(perms []string) {
	g := map[string]bool{}
	for _, perm := range perms {
		if p.meta.Requests(perm) {
			g[perm] = true
		}
	}
	p.stMu.Lock()
	p.grants = g
	p.stMu.Unlock()
}

func (p *Plugin) config() json.RawMessage {
	p.stMu.Lock()
	defer p.stMu.Unlock()
	return p.cfg
}

func (p *Plugin) setConfig(c json.RawMessage) {
	p.stMu.Lock()
	p.cfg = normConfig(c)
	p.stMu.Unlock()
}

// ResetFailure снимает состояние failed и пробует запустить заново.
func (p *Plugin) ResetFailure() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.inst != nil {
		_ = p.inst.Close(context.Background())
		p.inst = nil
	}
	p.stMu.Lock()
	p.failTimes = nil
	p.stMu.Unlock()
	if err := p.startLocked(); err != nil {
		p.fail(err)
		return err
	}
	return nil
}

func (p *Plugin) Info() Info {
	p.stMu.Lock()
	defer p.stMu.Unlock()
	g := make([]string, 0, len(p.grants))
	for _, perm := range p.meta.Permissions {
		if p.grants[perm] {
			g = append(g, perm)
		}
	}
	return Info{Meta: p.meta, File: p.file, SHA256: p.sha, State: p.state, LastError: p.lastErr, Restarts: p.restarts, Granted: g, Config: p.cfg}
}

// ---- host API ----

func jsonOut(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func errOut(format string, a ...any) []byte {
	return jsonOut(map[string]string{"error": fmt.Sprintf(format, a...)})
}

// hostCall — единственная точка входа плагина в хост. Работает внутри вызова плагина (мьютекс p.mu уже занят),
// поэтому не обращается к экземпляру и не берёт p.mu.
func (p *Plugin) hostCall(ctx context.Context, op string, arg []byte) []byte {
	need := map[string]string{
		"storage.get": PermStorage, "storage.set": PermStorage, "storage.delete": PermStorage,
		"timer.set": PermTimer, "timer.clear": PermTimer,
		"events.publish":           PermPublish,
		"http.fetch":               PermNetHTTP,
		"twitch.redemption.update": PermRedeem,
	}
	if perm, ok := need[op]; ok && !p.granted(perm) {
		p.log.Warn("operation denied: permission not granted", "op", op, "permission", perm)
		return errOut("permission denied: %s", perm)
	}
	switch op {
	case "log":
		p.opLog(arg)
		return nil
	case "storage.get", "storage.set", "storage.delete":
		return p.opStorage(ctx, op, arg)
	case "timer.set":
		return p.opTimerSet(arg)
	case "timer.clear":
		var a struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(arg, &a)
		p.clearTimer(a.ID)
		return nil
	case "events.publish":
		return p.opPublish(arg)
	case "http.fetch":
		return p.opFetch(arg)
	case "twitch.redemption.update":
		return p.opRedeem(ctx, arg)
	}
	return errOut("unknown op %q", op)
}

func (p *Plugin) opLog(arg []byte) {
	var l struct {
		Level  string         `json:"level"`
		Msg    string         `json:"msg"`
		Fields map[string]any `json:"fields"`
	}
	if json.Unmarshal(arg, &l) != nil {
		return
	}
	if len(l.Msg) > maxLogMsg {
		l.Msg = l.Msg[:maxLogMsg] + "…"
	}
	attrs := make([]any, 0, 2*len(l.Fields))
	for k, v := range l.Fields {
		if len(attrs) >= 2*maxLogFields {
			break
		}
		attrs = append(attrs, k, v)
	}
	lvl := slog.LevelInfo
	switch strings.ToLower(l.Level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	}
	p.log.Log(p.ctx, lvl, l.Msg, attrs...)
}

func (p *Plugin) opStorage(ctx context.Context, op string, arg []byte) []byte {
	if p.svc.Storage == nil {
		return errOut("storage is not available")
	}
	var a struct {
		Key   string          `json:"key"`
		Value json.RawMessage `json:"value"`
	}
	if err := json.Unmarshal(arg, &a); err != nil {
		return errOut("bad arguments: %v", err)
	}
	if a.Key == "" || len(a.Key) > maxKeyLen {
		return errOut("key: 1..%d characters", maxKeyLen)
	}
	switch op {
	case "storage.get":
		v, found, err := p.svc.Storage.Get(ctx, p.meta.ID, a.Key)
		if err != nil {
			return errOut("%v", err)
		}
		if !found {
			return jsonOut(map[string]any{"found": false})
		}
		return jsonOut(map[string]any{"found": true, "value": v})
	case "storage.set":
		if len(a.Value) == 0 {
			return errOut("value is required")
		}
		if err := p.svc.Storage.Set(ctx, p.meta.ID, a.Key, a.Value); err != nil {
			return errOut("%v", err)
		}
	case "storage.delete":
		if err := p.svc.Storage.Delete(ctx, p.meta.ID, a.Key); err != nil {
			return errOut("%v", err)
		}
	}
	return jsonOut(map[string]any{})
}

// opPublish — events.publish: публикует событие в объявленный топик этого плагина. Пространство имён задаёт хост (id плагина),
// а не аргумент вызова; данные проходят проверку формата и размера в брокере.
func (p *Plugin) opPublish(arg []byte) []byte {
	if p.svc.Events == nil {
		return errOut("events are not available")
	}
	var a struct {
		Topic  string          `json:"topic"`
		Data   json.RawMessage `json:"data"`
		Retain bool            `json:"retain"`
	}
	if err := json.Unmarshal(arg, &a); err != nil {
		return errOut("bad arguments: %v", err)
	}
	if err := p.svc.Events.Publish(p.meta.ID, a.Topic, a.Data, a.Retain); err != nil {
		if errors.Is(err, events.ErrRate) {
			p.log.Warn("events: rate limit exceeded, event dropped", "topic", a.Topic)
		}
		return errOut("%v", err)
	}
	return jsonOut(map[string]any{})
}

func (p *Plugin) opTimerSet(arg []byte) []byte {
	var a struct {
		ID     string `json:"id"`
		MS     int    `json:"ms"`
		Repeat bool   `json:"repeat"`
	}
	if err := json.Unmarshal(arg, &a); err != nil {
		return errOut("bad arguments: %v", err)
	}
	if a.ID == "" || len(a.ID) > 64 {
		return errOut("id: 1..64 characters")
	}
	if a.MS < minTimerMS || a.MS > maxTimerMS {
		return errOut("ms: %d..%d", minTimerMS, maxTimerMS)
	}
	p.tmMu.Lock()
	if old := p.timers[a.ID]; old != nil {
		old() // таймер с тем же id заменяется
		delete(p.timers, a.ID)
	} else if len(p.timers) >= maxTimers {
		p.tmMu.Unlock()
		return errOut("too many timers (max %d)", maxTimers)
	}
	ctx, cancel := context.WithCancel(p.ctx)
	p.timers[a.ID] = cancel
	p.tmMu.Unlock()
	d := time.Duration(a.MS) * time.Millisecond
	go func() {
		t := time.NewTimer(d)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				p.enqueue(EventTimer, map[string]string{"id": a.ID})
				if !a.Repeat {
					p.tmMu.Lock()
					// не трогаем запись, если её уже заменили новым таймером с тем же id
					if ctx.Err() == nil {
						delete(p.timers, a.ID)
					}
					p.tmMu.Unlock()
					return
				}
				t.Reset(d)
			}
		}
	}()
	return jsonOut(map[string]any{})
}

func (p *Plugin) clearTimer(id string) {
	p.tmMu.Lock()
	defer p.tmMu.Unlock()
	if c := p.timers[id]; c != nil {
		c()
		delete(p.timers, id)
	}
}

// opFetch запускает запрос в своей горутине и сразу возвращается: ответ придёт событием http.response.
func (p *Plugin) opFetch(arg []byte) []byte {
	var a fetchArgs
	if err := json.Unmarshal(arg, &a); err != nil {
		return errOut("bad arguments: %v", err)
	}
	if err := a.validate(); err != nil {
		return errOut("%v", err)
	}
	select {
	case p.infly <- struct{}{}:
	default:
		return errOut("too many requests in flight (max %d)", fetchMaxInfly)
	}
	go func() {
		defer func() { <-p.infly }()
		ctx, cancel := context.WithTimeout(p.ctx, fetchTimeout)
		defer cancel()
		res := a.do(ctx, p.http)
		if p.ctx.Err() == nil {
			p.enqueue(EventHTTPResponse, res)
		}
	}()
	return jsonOut(map[string]any{})
}

func (p *Plugin) opRedeem(ctx context.Context, arg []byte) []byte {
	if p.svc.Redeem == nil {
		return errOut("twitch is not available")
	}
	var a struct {
		RewardID string `json:"rewardId"`
		ID       string `json:"id"`
		Status   string `json:"status"`
	}
	if err := json.Unmarshal(arg, &a); err != nil {
		return errOut("bad arguments: %v", err)
	}
	if a.Status != "FULFILLED" && a.Status != "CANCELED" {
		return errOut("status: FULFILLED or CANCELED")
	}
	if a.RewardID == "" || a.ID == "" {
		return errOut("rewardId and id are required")
	}
	if err := p.svc.Redeem(ctx, a.RewardID, a.ID, a.Status); err != nil {
		return errOut("%v", err)
	}
	return jsonOut(map[string]any{})
}

// lineWriter превращает вывод плагина в stdout/stderr (паника Go, println) в строки лога.
type lineWriter struct {
	log *slog.Logger
	mu  sync.Mutex
	buf []byte
}

const maxLine = 4096

func (w *lineWriter) Write(b []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, b...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		w.emit(w.buf[:i])
		w.buf = w.buf[i+1:]
	}
	if len(w.buf) > maxLine {
		w.emit(w.buf)
		w.buf = nil
	}
	return len(b), nil
}

func (w *lineWriter) emit(line []byte) {
	s := strings.TrimRight(string(line), "\r")
	if s != "" {
		w.log.Warn("stderr", "line", s)
	}
}
