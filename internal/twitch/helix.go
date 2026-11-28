package twitch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Права для наград: читать погашения (EventSub) и управлять наградами (создание, смена статуса погашения).
const (
	ScopeReadRedemptions   = "channel:read:redemptions"
	ScopeManageRedemptions = "channel:manage:redemptions"
)

// Статусы погашения для UpdateRedemption.
const (
	RedemptionFulfilled = "FULFILLED"
	RedemptionCanceled  = "CANCELED" // баллы возвращаются зрителю
)

// ErrNeedScopes — у аккаунта стримера не хватает прав Twitch (награды, модерация чата): нужно подключить его заново.
var ErrNeedScopes = errors.New("streamer account needs new Twitch permissions: connect it again in Settings → Twitch")

// Права для модерации чата: удалять сообщения и банить (в том числе timeout) от имени стримера как модератора своего канала.
const (
	ScopeManageChatMessages = "moderator:manage:chat_messages"
	ScopeManageBans         = "moderator:manage:banned_users"
)

var redemptionScopes = []string{ScopeReadRedemptions, ScopeManageRedemptions}

// UseAPIBase подменяет https://api.twitch.tv (для тестов).
func (m *Manager) UseAPIBase(u string) { m.apiBase = strings.TrimRight(u, "/") }

// streamer возвращает действующий токен и данные стримера; проверяет, что для наград хватает прав.
func (m *Manager) streamer(ctx context.Context) (token, userID, clientID string, err error) {
	return m.streamerWith(ctx, redemptionScopes)
}

// streamerWith — то же с произвольным набором нужных прав: нехватка любого — ErrNeedScopes.
func (m *Manager) streamerWith(ctx context.Context, need []string) (token, userID, clientID string, err error) {
	acc, err := m.st.GetAccount(RoleStreamer)
	if err != nil {
		return "", "", "", err
	}
	if acc == nil {
		return "", "", "", ErrNoAccount
	}
	for _, s := range need {
		if !hasScope(acc.Scopes, s) {
			return "", "", "", ErrNeedScopes
		}
	}
	token, _, err = m.Token(ctx, RoleStreamer)
	if err != nil {
		return "", "", "", err
	}
	clientID, _, err = m.app()
	if err != nil {
		return "", "", "", err
	}
	return token, acc.UserID, clientID, nil
}

func hasScope(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// helix выполняет запрос к Helix API от имени стримера. out может быть nil (ответ без тела).
func (m *Manager) helix(ctx context.Context, method, path string, q url.Values, in, out any) error {
	return m.helixWith(ctx, redemptionScopes, method, path, q, in, out)
}

// helixWith — helix с явным набором нужных прав. Для /helix/moderation/ добавляет moderator_id: модератор — сам стример.
func (m *Manager) helixWith(ctx context.Context, need []string, method, path string, q url.Values, in, out any) error {
	token, userID, clientID, err := m.streamerWith(ctx, need)
	if err != nil {
		return err
	}
	if q == nil {
		q = url.Values{}
	}
	if q.Get("broadcaster_id") == "" && !strings.HasPrefix(path, "/helix/eventsub/") {
		q.Set("broadcaster_id", userID)
	}
	if strings.HasPrefix(path, "/helix/moderation/") && q.Get("moderator_id") == "" {
		q.Set("moderator_id", userID)
	}
	u := m.apiBase + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Client-Id", clientID)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := m.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		var e struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(b, &e)
		if e.Message == "" {
			e.Message = resp.Status
		}
		return &httpError{status: resp.StatusCode, message: e.Message}
	}
	if out == nil || len(b) == 0 {
		return nil
	}
	return json.Unmarshal(b, out)
}

// Reward — награда за баллы канала.
type Reward struct {
	ID            string `json:"id"`
	Title         string `json:"title"`
	Cost          int    `json:"cost"`
	Prompt        string `json:"prompt"`
	InputRequired bool   `json:"inputRequired"`
	Enabled       bool   `json:"enabled"`
	// Manageable — награда создана этим приложением (Client ID): только у таких можно менять статус погашений.
	Manageable bool `json:"manageable"`
}

type helixReward struct {
	ID            string `json:"id"`
	Title         string `json:"title"`
	Cost          int    `json:"cost"`
	Prompt        string `json:"prompt"`
	InputRequired bool   `json:"is_user_input_required"`
	Enabled       bool   `json:"is_enabled"`
}

func (h helixReward) reward(manageable bool) Reward {
	return Reward{ID: h.ID, Title: h.Title, Cost: h.Cost, Prompt: h.Prompt, InputRequired: h.InputRequired, Enabled: h.Enabled, Manageable: manageable}
}

// Rewards — награды канала. Две выборки: все и только «наши» (управляемые) — по второй помечается Manageable.
func (m *Manager) Rewards(ctx context.Context) ([]Reward, error) {
	var all, mine struct {
		Data []helixReward `json:"data"`
	}
	if err := m.helix(ctx, http.MethodGet, "/helix/channel_points/custom_rewards", nil, nil, &all); err != nil {
		return nil, err
	}
	if err := m.helix(ctx, http.MethodGet, "/helix/channel_points/custom_rewards", url.Values{"only_manageable_rewards": {"true"}}, nil, &mine); err != nil {
		return nil, err
	}
	own := map[string]bool{}
	for _, r := range mine.Data {
		own[r.ID] = true
	}
	out := make([]Reward, 0, len(all.Data))
	for _, r := range all.Data {
		out = append(out, r.reward(own[r.ID]))
	}
	return out, nil
}

// NewReward — параметры создаваемой награды.
type NewReward struct {
	Title         string
	Cost          int
	Prompt        string
	InputRequired bool
}

// ErrInvalidReward — параметры награды не прошли проверку (в отличие от ошибок Twitch и сети).
var ErrInvalidReward = errors.New("invalid reward")

type invalidReward string

func (e invalidReward) Error() string        { return string(e) }
func (e invalidReward) Is(target error) bool { return target == ErrInvalidReward }

// CreateReward создаёт награду (она сразу «наша»: ей можно менять статус погашений).
func (m *Manager) CreateReward(ctx context.Context, n NewReward) (Reward, error) {
	n.Title = strings.TrimSpace(n.Title)
	if n.Title == "" || len([]rune(n.Title)) > 45 {
		return Reward{}, invalidReward("reward title: 1..45 characters")
	}
	if n.Cost < 1 {
		return Reward{}, invalidReward("reward cost must be at least 1")
	}
	if len([]rune(n.Prompt)) > 200 {
		return Reward{}, invalidReward("reward prompt: at most 200 characters")
	}
	in := map[string]any{"title": n.Title, "cost": n.Cost, "prompt": n.Prompt, "is_user_input_required": n.InputRequired, "is_enabled": true}
	var out struct {
		Data []helixReward `json:"data"`
	}
	if err := m.helix(ctx, http.MethodPost, "/helix/channel_points/custom_rewards", nil, in, &out); err != nil {
		return Reward{}, err
	}
	if len(out.Data) == 0 {
		return Reward{}, errors.New("twitch returned no reward")
	}
	return out.Data[0].reward(true), nil
}

// UpdateRedemption меняет статус погашения: выполнено (FULFILLED) или отменено с возвратом баллов (CANCELED).
// Работает только для наград, созданных этим приложением (Client ID).
func (m *Manager) UpdateRedemption(ctx context.Context, rewardID, redemptionID, status string) error {
	if status != RedemptionFulfilled && status != RedemptionCanceled {
		return fmt.Errorf("bad status %q", status)
	}
	if rewardID == "" || redemptionID == "" {
		return errors.New("reward and redemption ids are required")
	}
	q := url.Values{"id": {redemptionID}, "reward_id": {rewardID}}
	return m.helix(ctx, http.MethodPatch, "/helix/channel_points/custom_rewards/redemptions", q, map[string]string{"status": status}, nil)
}

// ErrInvalidModeration — неверные аргументы модерации (идентификатор, длительность).
var ErrInvalidModeration = errors.New("invalid moderation request")

var twitchIDRe = regexp.MustCompile(`^[0-9A-Za-z_-]{1,64}$`)

const (
	maxTimeoutSeconds = 1209600 // 14 дней — предел Twitch
	maxReasonRunes    = 500
)

var moderationScopesMessages = []string{ScopeManageChatMessages}
var moderationScopesBans = []string{ScopeManageBans}

// DeleteChatMessage удаляет сообщение зрителя в чате канала стримера.
func (m *Manager) DeleteChatMessage(ctx context.Context, messageID string) error {
	if !twitchIDRe.MatchString(messageID) {
		return fmt.Errorf("%w: message id", ErrInvalidModeration)
	}
	return m.helixWith(ctx, moderationScopesMessages, http.MethodDelete, "/helix/moderation/chat", url.Values{"message_id": {messageID}}, nil, nil)
}

// Ban блокирует пользователя на seconds секунд (timeout: 1..1209600); seconds == 0 — бессрочный бан.
func (m *Manager) Ban(ctx context.Context, userID string, seconds int, reason string) error {
	if !twitchIDRe.MatchString(userID) {
		return fmt.Errorf("%w: user id", ErrInvalidModeration)
	}
	if seconds < 0 || seconds > maxTimeoutSeconds {
		return fmt.Errorf("%w: duration 1..%d seconds or 0 for a ban", ErrInvalidModeration, maxTimeoutSeconds)
	}
	if utf8.RuneCountInString(reason) > maxReasonRunes {
		return fmt.Errorf("%w: reason is too long", ErrInvalidModeration)
	}
	data := map[string]any{"user_id": userID}
	if seconds > 0 {
		data["duration"] = seconds
	}
	if reason != "" {
		data["reason"] = reason
	}
	return m.helixWith(ctx, moderationScopesBans, http.MethodPost, "/helix/moderation/bans", nil, map[string]any{"data": data}, nil)
}
