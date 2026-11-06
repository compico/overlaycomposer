// Package main (music) — плагин «заявки на треки» для серверных wasm-плагинов OC (PoC).
// Вся логика здесь, без привязки к ABI: она проверяется обычными Go-тестами, а ABI-обёртка — в abi_wasip1.go.
//
// Модель: плагин пассивен и событийный. Хост доставляет событие `twitch.redemption`, аддон плеера опрашивает маршруты
// (`GET /next`, `POST /started`, `POST /failed`) — обработчики возвращают ответ сразу, ничего не ждут. Очередь лежит в storage хоста.
package main

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Host — возможности хоста, нужные плагину.
type Host interface {
	Get(key string) string
	Set(key, value string)
	Log(level, msg string, kv map[string]any)
	// UpdateRedemption: status FULFILLED | CANCELED.
	UpdateRedemption(rewardID, redemptionID, status string)
}

type Config struct {
	RewardID string `json:"rewardId"`
}

type Redemption struct {
	ID       string `json:"id"`
	RewardID string `json:"rewardId"`
	User     string `json:"userLogin"`
	Input    string `json:"input"`
}

type Item struct {
	ID           int    `json:"id"`
	RedemptionID string `json:"redemptionId"`
	RewardID     string `json:"rewardId"`
	User         string `json:"user"`
	Input        string `json:"input"`
	TrackID      string `json:"trackId,omitempty"`
	AlbumID      string `json:"albumId,omitempty"`
	Status       string `json:"status"` // queued | sent | played | failed | rejected
	Error        string `json:"error,omitempty"`
	SentAt       int64  `json:"sentAt,omitempty"`
}

type state struct {
	NextID int    `json:"nextId"`
	Items  []Item `json:"items"`
}

const (
	maxQueued   = 100
	maxKept     = 200 // сколько заявок помним (дедупликация + история)
	maxInput    = 500
	storageKey  = "state"
	resendAfter = 120 // секунд: отданная, но не подтверждённая заявка отдаётся повторно с resend
)

var trackPathRe = regexp.MustCompile(`^/album/(\d{1,20})/track/(\d{1,20})/?$`)
var yandexHosts = map[string]bool{"music.yandex.ru": true, "music.yandex.com": true, "music.yandex.kz": true, "music.yandex.by": true}

func parseYandex(in string) (album, track string, ok bool) {
	if len(in) > 2048 {
		return
	}
	u, err := url.Parse(strings.TrimSpace(in))
	if err != nil || u.Scheme != "https" || u.User != nil || !yandexHosts[strings.ToLower(u.Hostname())] || (u.Port() != "" && u.Port() != "443") {
		return
	}
	m := trackPathRe.FindStringSubmatch(u.Path)
	if m == nil {
		return
	}
	return m[1], m[2], true
}

type Plugin struct {
	H   Host
	Cfg Config
	Now func() time.Time
}

func (p *Plugin) load() state {
	var s state
	if raw := p.H.Get(storageKey); raw != "" {
		_ = json.Unmarshal([]byte(raw), &s)
	}
	return s
}

func (p *Plugin) save(s state) {
	if len(s.Items) > maxKept { // отрезаем самые старые завершённые
		drop := len(s.Items) - maxKept
		kept := s.Items[:0]
		for _, it := range s.Items {
			if drop > 0 && it.Status != "queued" && it.Status != "sent" {
				drop--
				continue
			}
			kept = append(kept, it)
		}
		s.Items = kept
	}
	b, _ := json.Marshal(s)
	p.H.Set(storageKey, string(b))
}

// OnRedemption — событие twitch.redemption.
func (p *Plugin) OnRedemption(r Redemption) {
	if p.Cfg.RewardID == "" || r.RewardID != p.Cfg.RewardID {
		return
	}
	s := p.load()
	for _, it := range s.Items {
		if it.RedemptionID == r.ID {
			return // повторная доставка
		}
	}
	input := r.Input
	if rs := []rune(input); len(rs) > maxInput {
		input = string(rs[:maxInput])
	}
	s.NextID++
	it := Item{ID: s.NextID, RedemptionID: r.ID, RewardID: r.RewardID, User: r.User, Input: input, Status: "queued"}
	queued := 0
	for _, x := range s.Items {
		if x.Status == "queued" {
			queued++
		}
	}
	if album, track, ok := parseYandex(r.Input); !ok {
		it.Status, it.Error = "rejected", "not a Yandex Music track link"
	} else if queued >= maxQueued {
		it.Status, it.Error = "rejected", "the queue is full"
	} else {
		it.AlbumID, it.TrackID = album, track
	}
	s.Items = append(s.Items, it)
	p.save(s)
	if it.Status == "rejected" {
		p.H.UpdateRedemption(it.RewardID, it.RedemptionID, "CANCELED") // баллы возвращаются
		p.H.Log("info", "заявка отклонена", map[string]any{"user": it.User, "reason": it.Error})
		return
	}
	p.H.Log("info", "заявка принята", map[string]any{"id": it.ID, "user": it.User, "track": it.TrackID})
}

type Request struct {
	Method string            `json:"method"`
	Path   string            `json:"path"`
	Query  map[string]string `json:"query"`
	Body   json.RawMessage   `json:"body"`
}

type Response struct {
	Status int `json:"status"`
	Body   any `json:"body"`
}

func errResp(code int, msg string) Response { return Response{code, map[string]string{"error": msg}} }

// Handle — маршруты для аддона плеера.
func (p *Plugin) Handle(handler string, req Request) Response {
	switch handler {
	case "next":
		return p.next()
	case "started", "failed":
		var b struct {
			ID     int    `json:"id"`
			Reason string `json:"reason"`
		}
		if err := json.Unmarshal(req.Body, &b); err != nil || b.ID < 1 {
			return errResp(400, "body must be {\"id\": N}")
		}
		return p.finish(b.ID, handler == "started", b.Reason)
	case "queue":
		s := p.load()
		return Response{200, map[string]any{"items": s.Items}}
	}
	return errResp(404, "unknown handler "+handler)
}

// next отдаёт аддону заявку: уже отданную без подтверждения — повторно (resend), иначе самую раннюю из очереди.
// Опрос идемпотентен: повторный GET не продвигает очередь.
func (p *Plugin) next() Response {
	s := p.load()
	now := p.Now().Unix()
	for i := range s.Items {
		if s.Items[i].Status == "sent" {
			it := s.Items[i]
			s.Items[i].SentAt = now
			p.save(s)
			return Response{200, map[string]any{"item": it, "resend": true}}
		}
	}
	for i := range s.Items {
		if s.Items[i].Status == "queued" {
			s.Items[i].Status, s.Items[i].SentAt = "sent", now
			p.save(s)
			return Response{200, map[string]any{"item": s.Items[i], "resend": false}}
		}
	}
	return Response{200, map[string]any{"item": nil}}
}

func (p *Plugin) finish(id int, ok bool, reason string) Response {
	s := p.load()
	for i := range s.Items {
		it := &s.Items[i]
		if it.ID != id {
			continue
		}
		if ok && it.Status != "sent" {
			return errResp(409, "request was not sent to the player") // подтверждать можно только отданную
		}
		if it.Status != "sent" && it.Status != "queued" {
			return errResp(409, "request is already "+it.Status)
		}
		if ok {
			it.Status = "played"
			p.H.UpdateRedemption(it.RewardID, it.RedemptionID, "FULFILLED")
		} else {
			it.Status, it.Error = "failed", reason
			p.H.UpdateRedemption(it.RewardID, it.RedemptionID, "CANCELED")
		}
		p.save(s)
		return Response{200, map[string]any{"ok": true}}
	}
	return errResp(404, "unknown request")
}

// OnTimer — периодический тик: пишет в лог состояние очереди (демонстрация таймеров).
func (p *Plugin) OnTimer(id string) {
	s := p.load()
	counts := map[string]int{}
	for _, it := range s.Items {
		counts[it.Status]++
	}
	p.H.Log("info", "тик очереди", map[string]any{"queued": counts["queued"], "sent": counts["sent"], "played": counts["played"]})
}
