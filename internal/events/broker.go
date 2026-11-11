// Package events — брокер событий «плагин → подписчики» (pub/sub). Он ничего не знает ни о wasm, ни о сети: плагин публикует
// объявленный топик, подписчики (страница OC, внешний WebSocket) получают события из своей ограниченной очереди.
//
// Правила, на которых держится безопасность:
//   - топик всегда принадлежит плагину: публикует и читает его только по паре (плагин, топик), чужое пространство имён недоступно;
//   - публиковать и подписываться можно только на топики, которые плагин объявил в метаданных (Declare);
//   - данные хост не разбирает, лишь проверяет, что это JSON не больше MaxData;
//   - частота публикации ограничена на плагин (ведро токенов), лишнее отбрасывается с ошибкой, а не копится;
//   - медленный подписчик не тормозит издателя: при переполнении очереди теряются самые старые события.
package events

import (
	"encoding/json"
	"errors"
	"regexp"
	"sync"
	"time"
)

const (
	// MaxData — предел размера данных одного события.
	MaxData = 64 << 10
	// MaxTopics — сколько топиков плагин может объявить.
	MaxTopics = 32
	// DefaultQueue — размер очереди подписчика по умолчанию.
	DefaultQueue = 64
	maxQueue     = 1024
)

var topicRe = regexp.MustCompile(`^[a-z0-9._-]{1,64}$`)

var (
	ErrBadTopic    = errors.New("topic must be 1..64 characters of a-z, 0-9, '.', '_', '-'")
	ErrNotDeclared = errors.New("topic is not declared by the plugin")
	ErrTooLarge    = errors.New("event data is too large")
	ErrBadData     = errors.New("event data must be valid JSON")
	ErrRate        = errors.New("too many events")
	ErrClosed      = errors.New("subscription is closed")
)

// ValidTopic проверяет формат имени топика.
func ValidTopic(t string) bool { return topicRe.MatchString(t) }

// ValidateDeclared проверяет список объявленных топиков (для метаданных плагина).
func ValidateDeclared(topics []string) error {
	if len(topics) > MaxTopics {
		return errors.New("too many events (max 32)")
	}
	seen := map[string]bool{}
	for _, t := range topics {
		if !ValidTopic(t) {
			return ErrBadTopic
		}
		if seen[t] {
			return errors.New("duplicate event topic " + t)
		}
		seen[t] = true
	}
	return nil
}

// Event — доставленное подписчику событие. Seq растёт в пределах плагина и помогает клиенту заметить пропуски.
type Event struct {
	Plugin string          `json:"plugin"`
	Topic  string          `json:"topic"`
	Data   json.RawMessage `json:"data"`
	Seq    uint64          `json:"seq"`
	Retain bool            `json:"retained,omitempty"` // значение из хранилища, а не свежее событие
}

// Options — настройки брокера. Нулевые значения означают значения по умолчанию.
type Options struct {
	RatePerSec float64          // средняя частота публикации на плагин (по умолчанию 20)
	Burst      int              // всплеск (по умолчанию 40)
	Now        func() time.Time // источник времени (в тестах подменяется)
}

type bucket struct {
	tokens float64
	last   time.Time
}

type pluginState struct {
	declared map[string]bool
	retained map[string]Event
	seq      uint64
	rate     bucket
	subs     map[*Sub]struct{}
}

// Broker — брокер событий. Безопасен для одновременного использования.
type Broker struct {
	mu      sync.Mutex
	plugins map[string]*pluginState
	opt     Options
}

// New создаёт брокер.
func New(opt Options) *Broker {
	if opt.RatePerSec <= 0 {
		opt.RatePerSec = 20
	}
	if opt.Burst <= 0 {
		opt.Burst = 40
	}
	if opt.Now == nil {
		opt.Now = time.Now
	}
	return &Broker{plugins: map[string]*pluginState{}, opt: opt}
}

func (b *Broker) state(plugin string) *pluginState {
	st := b.plugins[plugin]
	if st == nil {
		st = &pluginState{declared: map[string]bool{}, retained: map[string]Event{}, subs: map[*Sub]struct{}{}}
		st.rate = bucket{tokens: float64(b.opt.Burst), last: b.opt.Now()}
		b.plugins[plugin] = st
	}
	return st
}

// Declare задаёт набор объявленных топиков плагина (при загрузке и перезагрузке). Retained-значения снятых топиков
// удаляются; подписки на снятые топики закрываются, потому что им больше ничего не придёт.
func (b *Broker) Declare(plugin string, topics []string) error {
	if err := ValidateDeclared(topics); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	st := b.state(plugin)
	next := make(map[string]bool, len(topics))
	for _, t := range topics {
		next[t] = true
	}
	for t := range st.retained {
		if !next[t] {
			delete(st.retained, t)
		}
	}
	for s := range st.subs {
		if !next[s.topic] {
			s.closeLocked()
			delete(st.subs, s)
		}
	}
	st.declared = next
	return nil
}

// Forget забывает плагин (выгружен): подписки закрываются, retained-значения удаляются.
func (b *Broker) Forget(plugin string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	st := b.plugins[plugin]
	if st == nil {
		return
	}
	for s := range st.subs {
		s.closeLocked()
	}
	delete(b.plugins, plugin)
}

func (st *pluginState) allow(now time.Time, ratePerSec float64, burst int) bool {
	if el := now.Sub(st.rate.last).Seconds(); el > 0 {
		st.rate.tokens += el * ratePerSec
		if st.rate.tokens > float64(burst) {
			st.rate.tokens = float64(burst)
		}
		st.rate.last = now
	}
	if st.rate.tokens < 1 {
		return false
	}
	st.rate.tokens--
	return true
}

// Publish публикует событие плагина. retain: хранить последнее значение топика для новых подписчиков.
func (b *Broker) Publish(plugin, topic string, data json.RawMessage, retain bool) error {
	if !ValidTopic(topic) {
		return ErrBadTopic
	}
	if len(data) > MaxData {
		return ErrTooLarge
	}
	if !json.Valid(data) {
		return ErrBadData
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	st := b.plugins[plugin]
	if st == nil || !st.declared[topic] {
		return ErrNotDeclared
	}
	if !st.allow(b.opt.Now(), b.opt.RatePerSec, b.opt.Burst) {
		return ErrRate
	}
	st.seq++
	ev := Event{Plugin: plugin, Topic: topic, Data: append(json.RawMessage(nil), data...), Seq: st.seq}
	if retain {
		r := ev
		r.Retain = true
		st.retained[topic] = r
	} else {
		delete(st.retained, topic) // новое событие без retain делает старое сохранённое значение недостоверным
	}
	for s := range st.subs {
		if s.topic == topic {
			s.push(ev)
		}
	}
	return nil
}

// Sub — подписка на один топик. События читаются из C; Close вызывает подписчик, когда соединение закрылось.
type Sub struct {
	C <-chan Event

	b       *Broker
	plugin  string
	topic   string
	ch      chan Event
	closed  bool
	dropped uint64
}

// Subscribe подписывает на объявленный топик плагина. Если у топика есть retained-значение, оно приходит первым.
func (b *Broker) Subscribe(plugin, topic string, queue int) (*Sub, error) {
	if !ValidTopic(topic) {
		return nil, ErrBadTopic
	}
	if queue <= 0 {
		queue = DefaultQueue
	}
	if queue > maxQueue {
		queue = maxQueue
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	st := b.plugins[plugin]
	if st == nil || !st.declared[topic] {
		return nil, ErrNotDeclared
	}
	ch := make(chan Event, queue)
	s := &Sub{C: ch, b: b, plugin: plugin, topic: topic, ch: ch}
	st.subs[s] = struct{}{}
	if r, ok := st.retained[topic]; ok {
		ch <- r
	}
	return s, nil
}

// push кладёт событие в очередь без блокировки; при переполнении выбрасывает самое старое. Вызывается под b.mu.
func (s *Sub) push(ev Event) {
	if s.closed {
		return
	}
	for {
		select {
		case s.ch <- ev:
			return
		default:
		}
		select {
		case <-s.ch:
			s.dropped++
		default:
		}
	}
}

// Dropped — сколько событий потеряно из-за переполнения очереди этого подписчика.
func (s *Sub) Dropped() uint64 {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	return s.dropped
}

func (s *Sub) closeLocked() {
	if !s.closed {
		s.closed = true
		close(s.ch)
	}
}

// Close снимает подписку; канал C закрывается. Повторный вызов безопасен.
func (s *Sub) Close() {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	if st := s.b.plugins[s.plugin]; st != nil {
		delete(st.subs, s)
	}
	s.closeLocked()
}
