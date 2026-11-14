package twitch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

const (
	eventSubURL    = "wss://eventsub.wss.twitch.tv/ws"
	redemptionType = "channel.channel_points_custom_reward_redemption.add"

	defaultKeepalive = 10 * time.Second // пока welcome не сообщил свой

	reconnectWait  = 30 * time.Second // сколько ждём welcome на новом адресе после session_reconnect
	keepaliveGrace = 5 * time.Second  // запас сверх keepalive_timeout_seconds, прежде чем считать соединение мёртвым
	seenMessages   = 256              // сколько последних message_id помним для отсечения повторов
)

// wsConn — то, что нужно от WebSocket-соединения; реальное — в ws.go, в тестах подставляется поддельное.
type wsConn interface {
	ReadMessage() ([]byte, error)
	SetReadDeadline(time.Time) error
	Close() error
}

type dialFunc func(ctx context.Context, url string) (wsConn, error)

// Redemption — погашение награды за баллы канала (нормализованное событие EventSub).
type Redemption struct {
	ID          string    `json:"id"`
	RewardID    string    `json:"rewardId"`
	RewardTitle string    `json:"rewardTitle"`
	UserID      string    `json:"userId"`
	UserLogin   string    `json:"userLogin"`
	UserName    string    `json:"userName"`
	Input       string    `json:"input"` // текст, который ввёл зритель (недоверенный)
	Status      string    `json:"status"`
	RedeemedAt  time.Time `json:"redeemedAt"`
}

// ES-состояния для индикатора в интерфейсе.
const (
	ESConnecting   = "connecting"
	ESConnected    = "connected"
	ESDisconnected = "disconnected"
	ESUnavailable  = "unavailable" // нет аккаунта стримера или не хватает прав
)

// EventSub — клиент Twitch EventSub по WebSocket: подписка на погашения наград, переподключение, отсечение повторов message_id.
// Повторная доставка одного погашения (после reconnect) возможна и под другим message_id: итоговая дедупликация по Redemption.ID — на стороне потребителя.
type EventSub struct {
	m        *Manager
	wsURL    string
	dialer   dialFunc
	grace    time.Duration // keepaliveGrace; в тестах короче
	onRedeem func(Redemption)
	onState  func(state, detail string)

	mu   sync.Mutex
	seen map[string]bool
	ring []string
}

// NewEventSub: onRedeem вызывается для каждого нового погашения (в одной горутине, по порядку), onState — при смене состояния.
func (m *Manager) NewEventSub(onRedeem func(Redemption), onState func(state, detail string)) *EventSub {
	if onState == nil {
		onState = func(string, string) {}
	}
	return &EventSub{m: m, wsURL: eventSubURL, dialer: dialWS, grace: keepaliveGrace, onRedeem: onRedeem, onState: onState, seen: map[string]bool{}}
}

// UseURL подменяет адрес EventSub (для тестов).
func (e *EventSub) UseURL(u string) { e.wsURL = u }

func (e *EventSub) firstSeen(id string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if id == "" {
		return true
	}
	if e.seen[id] {
		return false
	}
	e.seen[id] = true
	e.ring = append(e.ring, id)
	if len(e.ring) > seenMessages {
		delete(e.seen, e.ring[0])
		e.ring = e.ring[1:]
	}
	return true
}

type envelope struct {
	Metadata struct {
		MessageID   string `json:"message_id"`
		MessageType string `json:"message_type"`
	} `json:"metadata"`
	Payload struct {
		Session struct {
			ID               string `json:"id"`
			KeepaliveSeconds int    `json:"keepalive_timeout_seconds"`
			ReconnectURL     string `json:"reconnect_url"`
		} `json:"session"`
		Subscription struct {
			Type   string `json:"type"`
			Status string `json:"status"`
		} `json:"subscription"`
		Event json.RawMessage `json:"event"`
	} `json:"payload"`
}

type redemptionEvent struct {
	ID        string `json:"id"`
	UserID    string `json:"user_id"`
	UserLogin string `json:"user_login"`
	UserName  string `json:"user_name"`
	Input     string `json:"user_input"`
	Status    string `json:"status"`
	Reward    struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	} `json:"reward"`
	RedeemedAt time.Time `json:"redeemed_at"`
}

func parseRedemption(raw json.RawMessage) (Redemption, error) {
	var ev redemptionEvent
	if err := json.Unmarshal(raw, &ev); err != nil {
		return Redemption{}, err
	}
	if ev.ID == "" || ev.Reward.ID == "" {
		return Redemption{}, errors.New("redemption without id or reward")
	}
	return Redemption{
		ID: ev.ID, RewardID: ev.Reward.ID, RewardTitle: ev.Reward.Title,
		UserID: ev.UserID, UserLogin: ev.UserLogin, UserName: ev.UserName,
		Input: ev.Input, Status: ev.Status, RedeemedAt: ev.RedeemedAt,
	}, nil
}

// Run держит соединение, пока ctx не отменён: сам переподключается (с растущей паузой) и ждёт, пока появится аккаунт стримера с нужными правами.
func (e *EventSub) Run(ctx context.Context) {
	wait := time.Second
	for ctx.Err() == nil {
		e.onState(ESConnecting, "")
		reached, err := e.session(ctx)
		if ctx.Err() != nil {
			break
		}
		state := ESDisconnected
		if errors.Is(err, ErrNoAccount) || errors.Is(err, ErrNeedScopes) {
			state = ESUnavailable
			wait = 15 * time.Second // ждём, пока пользователь подключит аккаунт: без спама в лог
		} else {
			slog.Warn("twitch eventsub", "err", err)
			if reached {
				wait = time.Second
			}
		}
		detail := ""
		if err != nil {
			detail = err.Error()
		}
		e.onState(state, detail)
		select {
		case <-ctx.Done():
		case <-time.After(wait):
		}
		if state != ESUnavailable && wait < 30*time.Second {
			wait *= 2
		}
	}
	e.onState(ESDisconnected, "")
}

// session — одно соединение от подключения до разрыва. reached — дошли ли до подписки (для сброса паузы).
func (e *EventSub) session(ctx context.Context) (reached bool, err error) {
	// До подключения проверяем аккаунт и права: нет смысла открывать сокет, который мы не сможем подписать.
	if _, _, _, err := e.m.streamer(ctx); err != nil {
		return false, err
	}
	conn, err := e.dialer(ctx, e.wsURL)
	if err != nil {
		return false, err
	}
	stop := make(chan struct{})
	defer close(stop)
	var curMu sync.Mutex
	cur := conn
	go func() { // отмена контекста закрывает сокет и будит ReadMessage
		select {
		case <-ctx.Done():
			curMu.Lock()
			cur.Close()
			curMu.Unlock()
		case <-stop:
		}
	}()
	defer func() {
		curMu.Lock()
		cur.Close()
		curMu.Unlock()
	}()

	keepalive := defaultKeepalive
	subscribed := false
	for {
		_ = conn.SetReadDeadline(time.Now().Add(keepalive + e.grace))
		data, err := conn.ReadMessage()
		if err != nil {
			if ctx.Err() != nil {
				return subscribed, ctx.Err()
			}
			return subscribed, fmt.Errorf("eventsub read: %w", err)
		}
		var env envelope
		if err := json.Unmarshal(data, &env); err != nil {
			slog.Warn("twitch eventsub: битое сообщение", "err", err)
			continue
		}
		if !e.firstSeen(env.Metadata.MessageID) {
			continue
		}
		switch env.Metadata.MessageType {
		case "session_welcome":
			if s := env.Payload.Session.KeepaliveSeconds; s > 0 {
				keepalive = time.Duration(s) * time.Second
			}
			if !subscribed {
				if err := e.subscribe(ctx, env.Payload.Session.ID); err != nil {
					return false, err
				}
				subscribed = true
				e.onState(ESConnected, "")
			}
		case "session_reconnect":
			// Новый сокет поднимаем, пока старый ещё живёт: события между ними не теряются. Подписки переходят сами.
			next, err := e.reconnect(ctx, env.Payload.Session.ReconnectURL)
			if err != nil {
				return subscribed, err
			}
			curMu.Lock()
			old := cur
			cur = next
			curMu.Unlock()
			old.Close()
			conn = next
		case "notification":
			if env.Payload.Subscription.Type != redemptionType {
				continue
			}
			r, err := parseRedemption(env.Payload.Event)
			if err != nil {
				slog.Warn("twitch eventsub: погашение не разобрано", "err", err)
				continue
			}
			e.onRedeem(r)
		case "revocation":
			return subscribed, fmt.Errorf("eventsub subscription revoked: %s", env.Payload.Subscription.Status)
		case "session_keepalive":
		}
	}
}

// reconnect подключается к адресу из session_reconnect и ждёт на нём welcome.
func (e *EventSub) reconnect(ctx context.Context, url string) (wsConn, error) {
	if url == "" {
		return nil, errors.New("session_reconnect without url")
	}
	c, err := e.dialer(ctx, url)
	if err != nil {
		return nil, err
	}
	_ = c.SetReadDeadline(time.Now().Add(reconnectWait))
	for {
		data, err := c.ReadMessage()
		if err != nil {
			c.Close()
			return nil, fmt.Errorf("eventsub reconnect: %w", err)
		}
		var env envelope
		if json.Unmarshal(data, &env) == nil && env.Metadata.MessageType == "session_welcome" {
			e.firstSeen(env.Metadata.MessageID)
			return c, nil
		}
	}
}

func (e *EventSub) subscribe(ctx context.Context, sessionID string) error {
	if sessionID == "" {
		return errors.New("welcome without session id")
	}
	acc, err := e.m.st.GetAccount(RoleStreamer)
	if err != nil {
		return err
	}
	if acc == nil {
		return ErrNoAccount
	}
	body := map[string]any{
		"type": redemptionType, "version": "1",
		"condition": map[string]string{"broadcaster_user_id": acc.UserID},
		"transport": map[string]string{"method": "websocket", "session_id": sessionID},
	}
	// 202 Accepted — тоже успех; helix принимает любой 2xx.
	return e.m.helix(ctx, http.MethodPost, "/helix/eventsub/subscriptions", nil, body, nil)
}
