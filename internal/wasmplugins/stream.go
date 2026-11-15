package wasmplugins

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/fasthttp/websocket"
)

// Потоки net.local: хост держит websocket к локальной программе (например, tosu) и публикует выбранные поля кадров событием
// плагина. Плагин (wasm) при этом не вызывается, поэтому нагрузка на процессор ничтожна, а подключается хост без заголовка Origin,
// как обычный клиент: из sandbox-iframe с «Origin: null» программы вроде tosu такое соединение отвергают.
const (
	streamIdleCheck = time.Second // как часто смотрим, появился ли подписчик, пока потока нет
	streamRetryMin  = time.Second // пауза перед переподключением; растёт вдвое до streamRetryMax
	streamRetryMax  = 15 * time.Second
	streamDial      = 5 * time.Second
	streamReadLimit = 4 << 20 // кадр tosu v2 — десятки КБ; больше 4 МиБ — не наш случай
	streamSilence   = 60 * time.Second
)

// startStreams запускает по горутине на каждый объявленный поток. Горутины живут до остановки плагина (p.ctx).
func (p *Plugin) startStreams() {
	if p.svc.Events == nil {
		return
	}
	for _, t := range p.meta.NetLocal {
		for _, s := range t.Streams {
			go p.runStream(t, s)
		}
	}
}

func (p *Plugin) wantStream(s NetLocalStream) bool {
	return p.granted(PermNetLocal) && p.svc.Events.Subscribers(p.meta.ID, s.Topic) > 0
}

func (p *Plugin) runStream(t NetLocalTarget, s NetLocalStream) {
	retry := streamRetryMin
	for p.ctx.Err() == nil {
		if !p.wantStream(s) {
			retry = streamRetryMin
			if !sleepCtx(p.ctx, streamIdleCheck) {
				return
			}
			continue
		}
		start := time.Now()
		err := p.streamOnce(t, s)
		if p.ctx.Err() != nil {
			return
		}
		if time.Since(start) > streamRetryMax { // соединение было живым — начинаем паузы заново
			retry = streamRetryMin
		}
		if err != nil {
			p.log.Debug("stream ended", "topic", s.Topic, "err", err)
		}
		if !sleepCtx(p.ctx, retry) {
			return
		}
		if retry *= 2; retry > streamRetryMax {
			retry = streamRetryMax
		}
	}
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	tm := time.NewTimer(d)
	defer tm.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-tm.C:
		return true
	}
}

// streamOnce — одно соединение: от подключения до обрыва или до ухода последнего подписчика.
func (p *Plugin) streamOnce(t NetLocalTarget, s NetLocalStream) error {
	addr, ok := p.targetAddr(t)
	if !ok {
		return errors.New("bad target address")
	}
	u := url.URL{Scheme: "ws", Host: addr, Path: s.Path}
	d := websocket.Dialer{
		HandshakeTimeout: streamDial,
		NetDialContext:   (&net.Dialer{Timeout: streamDial, Control: loopbackOnly}).DialContext,
		Proxy:            nil, // прокси мог бы обойти проверку адреса
	}
	ctx, cancel := context.WithCancel(p.ctx)
	defer cancel()
	dctx, dcancel := context.WithTimeout(ctx, streamDial)
	conn, _, err := d.DialContext(dctx, u.String(), nil) // заголовков нет: Origin не отправляется
	dcancel()
	if err != nil {
		return err
	}
	defer conn.Close()
	conn.SetReadLimit(streamReadLimit)

	// Сторож: закрывает соединение, когда подписчиков не осталось, право отозвано или плагин остановлен.
	go func() {
		tk := time.NewTicker(streamIdleCheck)
		defer tk.Stop()
		for {
			select {
			case <-ctx.Done():
				_ = conn.Close()
				return
			case <-tk.C:
				if !p.wantStream(s) {
					_ = conn.Close()
					return
				}
			}
		}
	}()

	var last time.Time
	var prev []byte
	gap := s.gap()
	for {
		_ = conn.SetReadDeadline(time.Now().Add(streamSilence))
		typ, msg, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		now := time.Now()
		if typ != websocket.TextMessage || now.Sub(last) < gap {
			continue // слишком частые кадры отбрасываем, не разбирая JSON
		}
		out, err := extractFields(msg, s.Fields)
		if err != nil {
			p.log.Debug("stream frame skipped", "topic", s.Topic, "err", err)
			continue
		}
		last = now
		if bytes.Equal(out, prev) {
			continue
		}
		if err := p.svc.Events.Publish(p.meta.ID, s.Topic, out, true); err != nil {
			p.log.Debug("stream publish failed", "topic", s.Topic, "err", err)
			continue
		}
		prev = out
	}
}

// extractFields достаёт из JSON-объекта поля по путям «a.b.c» и собирает из них объект {имя: значение}. Нет поля — null.
// Верхний уровень разбирается один раз, дальше идём только по нужным поддеревьям.
func extractFields(raw []byte, fields map[string]string) (json.RawMessage, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return nil, err
	}
	subtrees := map[string]map[string]json.RawMessage{}
	out := make(map[string]json.RawMessage, len(fields))
	for name, path := range fields {
		cur, ok := top, true
		var val json.RawMessage
		segs := strings.Split(path, ".")
		for i, seg := range segs {
			v, found := cur[seg]
			if !found {
				ok = false
				break
			}
			if i == len(segs)-1 {
				val = v
				break
			}
			key := strings.Join(segs[:i+1], ".")
			next, cached := subtrees[key]
			if !cached {
				if json.Unmarshal(v, &next) != nil {
					next = nil
				}
				subtrees[key] = next
			}
			if next == nil {
				ok = false
				break
			}
			cur = next
		}
		if !ok {
			val = json.RawMessage("null")
		}
		out[name] = val
	}
	return json.Marshal(out)
}
