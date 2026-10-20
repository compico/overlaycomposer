// Package twitch — аккаунты Twitch (стример и бот): OAuth Authorization Code Flow с редиректом на localhost,
// хранение и автоматическое обновление токенов. Токены наружу (в UI и плагины) не отдаются: ими пользуется только сервер.
package twitch

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	RoleStreamer = "streamer"
	RoleBot      = "bot"

	keyClientID     = "twitch_client_id"
	keyClientSecret = "twitch_client_secret"

	stateTTL      = 10 * time.Minute
	refreshMargin = 5 * time.Minute
)

// Scopes — права по ролям. Чат читается и пишется через IRC (chat:read / chat:edit); больше не просим.
var Scopes = map[string][]string{
	RoleStreamer: {"chat:read", "chat:edit"},
	RoleBot:      {"chat:read", "chat:edit"},
}

var (
	ErrNotConfigured = errors.New("twitch app is not configured (client id / secret)")
	ErrNoAccount     = errors.New("account is not connected")
	ErrBadRole       = errors.New("unknown role")
	ErrBadState      = errors.New("unknown or expired state, start the connection again")
)

// Account — аккаунт, привязанный к роли. Токены внутри хранятся, но в JSON не попадают.
type Account struct {
	Role         string    `json:"role"`
	UserID       string    `json:"userId"`
	Login        string    `json:"login"`
	DisplayName  string    `json:"displayName"`
	Scopes       []string  `json:"scopes"`
	ConnectedAt  time.Time `json:"connectedAt"`
	LastError    string    `json:"error,omitempty"`
	AccessToken  string    `json:"-"`
	RefreshToken string    `json:"-"`
	ExpiresAt    time.Time `json:"-"`
}

// Storage — то, что нужно от базы.
type Storage interface {
	Setting(key string) (string, bool, error)
	SetSetting(key, value string) error
	ListAccounts() ([]Account, error)
	GetAccount(role string) (*Account, error)
	SaveAccount(Account) error
	DeleteAccount(role string) error
}

type pending struct {
	role    string
	expires time.Time
}

type Manager struct {
	st     Storage
	http   *http.Client
	idBase string // https://id.twitch.tv (в тестах — локальный сервер)
	now    func() time.Time

	mu      sync.Mutex
	pending map[string]pending // state → роль
	refresh sync.Mutex         // обновление токена — по одному
}

func New(st Storage) *Manager {
	return &Manager{
		st: st, http: &http.Client{Timeout: 15 * time.Second}, idBase: "https://id.twitch.tv",
		now: time.Now, pending: map[string]pending{},
	}
}

// ---- приложение (Client ID / Secret) ----

func (m *Manager) app() (id, secret string, err error) {
	id, _, err = m.st.Setting(keyClientID)
	if err != nil {
		return
	}
	secret, _, err = m.st.Setting(keyClientSecret)
	return
}

// AppInfo — что известно о приложении Twitch (секрет не возвращается).
func (m *Manager) AppInfo() (clientID string, hasSecret bool, err error) {
	id, secret, err := m.app()
	return id, secret != "", err
}

// SetApp сохраняет Client ID и (если не пустой) Client Secret. Смена Client ID отключает аккаунты: их токены выданы другому приложению.
func (m *Manager) SetApp(clientID, clientSecret string) error {
	clientID = strings.TrimSpace(clientID)
	clientSecret = strings.TrimSpace(clientSecret)
	if clientID == "" {
		return errors.New("client id is empty")
	}
	if strings.ContainsAny(clientID, " \t\r\n&=?#") {
		return errors.New("client id looks invalid")
	}
	old, _, err := m.app()
	if err != nil {
		return err
	}
	if clientID != old {
		accs, err := m.st.ListAccounts()
		if err != nil {
			return err
		}
		for _, a := range accs {
			if err := m.st.DeleteAccount(a.Role); err != nil {
				return err
			}
		}
		if clientSecret == "" {
			if err := m.st.SetSetting(keyClientSecret, ""); err != nil {
				return err
			}
		}
	}
	if err := m.st.SetSetting(keyClientID, clientID); err != nil {
		return err
	}
	if clientSecret != "" {
		return m.st.SetSetting(keyClientSecret, clientSecret)
	}
	return nil
}

// Accounts — подключённые аккаунты (без токенов в JSON).
func (m *Manager) Accounts() ([]Account, error) { return m.st.ListAccounts() }

// ---- подключение ----

// AuthURL начинает подключение роли: возвращает адрес страницы согласия Twitch.
func (m *Manager) AuthURL(role, redirectURI string) (string, error) {
	scopes, ok := Scopes[role]
	if !ok {
		return "", ErrBadRole
	}
	role = strings.Clone(role) // строка может указывать в переиспользуемый буфер HTTP-запроса
	id, secret, err := m.app()
	if err != nil {
		return "", err
	}
	if id == "" || secret == "" {
		return "", ErrNotConfigured
	}
	state := randHex(16)
	m.mu.Lock()
	now := m.now()
	for k, p := range m.pending { // подчищаем просроченные
		if now.After(p.expires) {
			delete(m.pending, k)
		}
	}
	m.pending[state] = pending{role: role, expires: now.Add(stateTTL)}
	m.mu.Unlock()
	q := url.Values{
		"client_id":     {id},
		"redirect_uri":  {redirectURI},
		"response_type": {"code"},
		"scope":         {strings.Join(scopes, " ")},
		"state":         {state},
		"force_verify":  {"true"}, // всегда спрашивать, какой аккаунт: в браузере может быть выполнен вход под другим
	}
	return m.idBase + "/oauth2/authorize?" + q.Encode(), nil
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// Callback завершает подключение: обменивает code на токены, проверяет их у Twitch и сохраняет аккаунт.
func (m *Manager) Callback(ctx context.Context, state, code, redirectURI string) (*Account, error) {
	m.mu.Lock()
	p, ok := m.pending[state]
	delete(m.pending, state) // state одноразовый
	m.mu.Unlock()
	if !ok || m.now().After(p.expires) {
		return nil, ErrBadState
	}
	id, secret, err := m.app()
	if err != nil {
		return nil, err
	}
	tok, err := m.tokenRequest(ctx, url.Values{
		"client_id": {id}, "client_secret": {secret}, "code": {code},
		"grant_type": {"authorization_code"}, "redirect_uri": {redirectURI},
	})
	if err != nil {
		return nil, err
	}
	v, err := m.validate(ctx, tok.AccessToken)
	if err != nil {
		return nil, err
	}
	acc := Account{
		Role: p.role, UserID: v.UserID, Login: v.Login, DisplayName: v.Login,
		Scopes: v.Scopes, ConnectedAt: m.now(),
		AccessToken: tok.AccessToken, RefreshToken: tok.RefreshToken,
		ExpiresAt: m.now().Add(time.Duration(tok.ExpiresIn) * time.Second),
	}
	if err := m.st.SaveAccount(acc); err != nil {
		return nil, err
	}
	return &acc, nil
}

// Disconnect забывает аккаунт и (по возможности) отзывает его токен у Twitch.
func (m *Manager) Disconnect(ctx context.Context, role string) error {
	if _, ok := Scopes[role]; !ok {
		return ErrBadRole
	}
	acc, err := m.st.GetAccount(role)
	if err != nil {
		return err
	}
	if acc == nil {
		return nil
	}
	if id, _, _ := m.app(); id != "" {
		form := url.Values{"client_id": {id}, "token": {acc.AccessToken}}
		if req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.idBase+"/oauth2/revoke", strings.NewReader(form.Encode())); err == nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			if resp, err := m.http.Do(req); err == nil {
				resp.Body.Close()
			}
		}
	}
	return m.st.DeleteAccount(role)
}

// ---- токены ----

// Token возвращает действующий access-токен роли (при необходимости обновляет). Для внутреннего использования сервером.
func (m *Manager) Token(ctx context.Context, role string) (token, login string, err error) {
	m.refresh.Lock()
	defer m.refresh.Unlock()
	acc, err := m.st.GetAccount(role)
	if err != nil {
		return "", "", err
	}
	if acc == nil {
		return "", "", ErrNoAccount
	}
	if m.now().Add(refreshMargin).Before(acc.ExpiresAt) {
		return acc.AccessToken, acc.Login, nil
	}
	id, secret, err := m.app()
	if err != nil {
		return "", "", err
	}
	tok, err := m.tokenRequest(ctx, url.Values{
		"client_id": {id}, "client_secret": {secret},
		"grant_type": {"refresh_token"}, "refresh_token": {acc.RefreshToken},
	})
	if err != nil {
		var he *httpError
		if errors.As(err, &he) && (he.status == 400 || he.status == 401) {
			// refresh-токен недействителен (отозван пользователем, смена пароля, прошло 30 дней) — нужно переподключить
			acc.LastError = "reconnect required: " + he.message
			_ = m.st.SaveAccount(*acc)
		}
		return "", "", err
	}
	acc.AccessToken = tok.AccessToken
	if tok.RefreshToken != "" {
		acc.RefreshToken = tok.RefreshToken
	}
	acc.ExpiresAt = m.now().Add(time.Duration(tok.ExpiresIn) * time.Second)
	acc.LastError = ""
	if err := m.st.SaveAccount(*acc); err != nil {
		return "", "", err
	}
	return acc.AccessToken, acc.Login, nil
}

type tokenResp struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
}

type httpError struct {
	status  int
	message string
}

func (e *httpError) Error() string { return fmt.Sprintf("twitch: %d %s", e.status, e.message) }

func (m *Manager) tokenRequest(ctx context.Context, form url.Values) (*tokenResp, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.idBase+"/oauth2/token", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var t tokenResp
	if err := m.doJSON(req, &t); err != nil {
		return nil, err
	}
	if t.AccessToken == "" {
		return nil, errors.New("twitch: empty access token")
	}
	return &t, nil
}

type validation struct {
	UserID string   `json:"user_id"`
	Login  string   `json:"login"`
	Scopes []string `json:"scopes"`
}

func (m *Manager) validate(ctx context.Context, token string) (*validation, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.idBase+"/oauth2/validate", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "OAuth "+token)
	var v validation
	if err := m.doJSON(req, &v); err != nil {
		return nil, err
	}
	if v.UserID == "" || v.Login == "" {
		return nil, errors.New("twitch: validate returned no user")
	}
	return &v, nil
}

func (m *Manager) doJSON(req *http.Request, out any) error {
	resp, err := m.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(b, &e)
		if e.Message == "" {
			e.Message = http.StatusText(resp.StatusCode)
		}
		return &httpError{status: resp.StatusCode, message: e.Message}
	}
	return json.Unmarshal(b, out)
}

// UseBaseURL подменяет адрес id.twitch.tv (для тестов с поддельным сервером).
func (m *Manager) UseBaseURL(u string) { m.idBase = strings.TrimRight(u, "/") }
