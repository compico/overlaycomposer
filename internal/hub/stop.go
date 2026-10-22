package hub

import (
	"encoding/json"
	"log/slog"
	"sync"
	"time"
)

// Плавная остановка: перед выключением сервера все подключённые страницы (оверлей, редактор) получают `app:stopping`,
// передают это своим плагинам (oc.lifecycle.onStop), ждут, пока те закончат (например, сохранят данные через oc.storage),
// и отвечают `app:stopped`. Пока ответы не пришли (или не вышло время), сервер продолжает работать — запросы плагинов
// к хранилищу и чату обрабатываются как обычно.

// StopMargin — запас сверх grace, который сервер ждёт ответы страниц (страница сама ограничивает плагины значением grace).
const StopMargin = time.Second

type stopWait struct {
	id      int
	pending map[*Peer]struct{}
	done    chan struct{}
	closed  bool
}

func (w *stopWait) finish() {
	if !w.closed {
		w.closed = true
		close(w.done)
	}
}

// stopState хранит текущее ожидание (guarded by stopMu).
type stopState struct {
	mu   sync.Mutex
	next int
	cur  *stopWait
}

// StopResult — итог плавной остановки.
type StopResult struct {
	Notified int  // сколько страниц уведомлено
	TimedOut int  // сколько не ответили вовремя
	Waited   bool // ждали ли вообще
}

// NotifyStopping уведомляет все страницы и ждёт их ответов не дольше grace+StopMargin. Возвращает, когда ответили все
// (отключившиеся страницы считаются ответившими) или вышло время.
func (h *Hub) NotifyStopping(grace time.Duration) StopResult {
	peers := h.Peers()
	if len(peers) == 0 {
		return StopResult{}
	}
	w := &stopWait{pending: map[*Peer]struct{}{}, done: make(chan struct{})}
	h.stop.mu.Lock()
	h.stop.next++
	w.id = h.stop.next
	h.stop.cur = w
	for _, p := range peers {
		w.pending[p] = struct{}{}
	}
	h.stop.mu.Unlock()

	msg := map[string]any{"type": "app:stopping", "id": w.id, "graceMs": grace.Milliseconds()}
	var lost []*Peer
	for _, p := range peers {
		if !p.Send(msg) {
			lost = append(lost, p) // клиент не успевает — ждать его бессмысленно
		}
	}
	h.stop.mu.Lock()
	for _, p := range lost {
		delete(w.pending, p)
	}
	if len(w.pending) == 0 {
		w.finish()
	}
	h.stop.mu.Unlock()

	res := StopResult{Notified: len(peers) - len(lost), Waited: true}
	timer := time.NewTimer(grace + StopMargin)
	defer timer.Stop()
	select {
	case <-w.done:
	case <-timer.C:
	}
	h.stop.mu.Lock()
	res.TimedOut = len(w.pending)
	h.stop.cur = nil
	h.stop.mu.Unlock()
	if res.TimedOut > 0 {
		slog.Warn("hub: страницы не успели ответить на остановку", "pages", res.TimedOut)
	}
	return res
}

// peerStopped отмечает, что страница ответила (или отключилась).
func (h *Hub) peerStopped(p *Peer, id int) {
	h.stop.mu.Lock()
	defer h.stop.mu.Unlock()
	w := h.stop.cur
	if w == nil || (id != 0 && id != w.id) {
		return
	}
	delete(w.pending, p)
	if len(w.pending) == 0 {
		w.finish()
	}
}

func (h *Hub) handleStopped(p *Peer, raw []byte) {
	var m struct {
		ID int `json:"id"`
	}
	if json.Unmarshal(raw, &m) != nil || m.ID == 0 {
		return
	}
	h.peerStopped(p, m.ID)
}

// Peers — снимок подключённых клиентов.
func (h *Hub) Peers() []*Peer {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]*Peer, 0, len(h.peers))
	for p := range h.peers {
		out = append(out, p)
	}
	return out
}
