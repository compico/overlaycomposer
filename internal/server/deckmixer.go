package server

import (
	"encoding/json"
	"log/slog"
	"time"

	"github.com/compico/overlaycomposer/internal/obs"
)

// Микшер в панели Deck (группа kind=mixer, #234). Устройство по WebSocket панели /api/deck/ws
//
//	клиент → {"t":"mixer","on":true|false}                 подписаться на уровни / отписаться (в фоне вкладки не нужны)
//	клиент → {"t":"mx","a":"toggle|monitor","n":"Mic"}     mute / прослушивание источника
//	клиент → {"t":"mx","a":"vol","n":"Mic","db":-12.5}     громкость в дБ (не выше 0)
//	сервер → {"t":"mixer","inputs":[{"name","peak","muted","volumeDb","monitor"}]}   около 20 раз в секунду
//
// Подписка и команды принимаются, только пока в сохранённой раскладке есть группа-микшер, и только для активных источников
// (это проверяет OBS-клиент). Команды ограничены по частоте на соединение: ползунок не завалит OBS запросами.
const deckMixerMinGap = 15 * time.Millisecond

type mixerFrame struct {
	T      string           `json:"t"`
	Inputs []obs.MixerInput `json:"inputs"`
}

// deckMixConn — микшерная часть одного WebSocket-соединения панели.
type deckMixConn struct {
	s       *Server
	ch      chan []obs.MixerInput // последний кадр уровней для писателя соединения (старые выбрасываются)
	stop    func()
	lastCmd time.Time
}

func newDeckMixConn(s *Server) *deckMixConn {
	return &deckMixConn{s: s, ch: make(chan []obs.MixerInput, 1)}
}

// push не блокирует OBS-клиент: медленной панели достаётся только последний кадр.
func (m *deckMixConn) push(in []obs.MixerInput) {
	select {
	case m.ch <- in:
	default:
		select {
		case <-m.ch:
		default:
		}
		select {
		case m.ch <- in:
		default:
		}
	}
}

func (m *deckMixConn) close() {
	if m.stop != nil {
		m.stop()
		m.stop = nil
	}
}

func (m *deckMixConn) allowed() bool {
	l, _ := m.s.deckLayout()
	return l.HasMixer()
}

// handle разбирает сообщение клиента; чужие и неисправные сообщения молча пропускает.
func (m *deckMixConn) handle(raw []byte) {
	var msg struct {
		T  string  `json:"t"`
		On bool    `json:"on"`
		A  string  `json:"a"`
		N  string  `json:"n"`
		DB float64 `json:"db"`
	}
	if json.Unmarshal(raw, &msg) != nil {
		return
	}
	o := m.s.deckObs()
	switch msg.T {
	case "mixer":
		if !msg.On {
			m.close()
			return
		}
		if m.stop == nil && o != nil && m.allowed() {
			m.stop = o.WatchMixer(m.push)
		}
	case "mx":
		if o == nil || m.stop == nil || !m.allowed() {
			return
		}
		now := time.Now()
		if now.Sub(m.lastCmd) < deckMixerMinGap {
			return
		}
		m.lastCmd = now
		var err error
		switch msg.A {
		case "toggle":
			err = o.ToggleInputMute(msg.N)
		case "monitor":
			err = o.ToggleInputMonitor(msg.N)
		case "vol":
			err = o.SetInputVolumeDB(msg.N, msg.DB)
		}
		if err != nil {
			slog.Warn("deck mixer: команда не выполнена", "action", msg.A, "err", err)
		}
	}
}

// frame — готовое сообщение для устройства.
func (m *deckMixConn) frame(in []obs.MixerInput) []byte {
	if in == nil {
		in = []obs.MixerInput{}
	}
	b, _ := json.Marshal(mixerFrame{T: "mixer", Inputs: in})
	return b
}
