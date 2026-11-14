package twitch

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Device Code Grant Flow для Public-приложения Twitch: секрет не нужен, браузер на этом компьютере не обязателен.
// Приложение получает код и ссылку, пользователь открывает ссылку (где угодно, где выполнен вход нужным аккаунтом) и
// подтверждает доступ, а фоновый опрос забирает токены.

// Статусы подключения по коду.
const (
	DevicePending = "pending"
	DeviceDone    = "done"
	DeviceFailed  = "error"
)

// DeviceStart — что показать пользователю.
type DeviceStart struct {
	Flow            string `json:"flow"`
	UserCode        string `json:"userCode"`
	VerificationURI string `json:"verificationUri"`
	ExpiresIn       int    `json:"expiresIn"`
}

// DeviceState — как идёт подключение.
type DeviceState struct {
	Role   string `json:"role"`
	Status string `json:"status"`
	Login  string `json:"login,omitempty"`
	Error  string `json:"error,omitempty"`
}

type deviceFlow struct {
	state   DeviceState
	cancel  context.CancelFunc
	expires time.Time
}

type deviceStore struct {
	mu    sync.Mutex
	flows map[string]*deviceFlow
}

// OnConnect задаёт функцию, которую вызывают после успешного подключения аккаунта по коду (перезапуск чата, уведомления).
func (m *Manager) OnConnect(fn func(Account)) { m.onConnect = fn }

// StartDevice начинает подключение роли по коду. Новое подключение той же роли отменяет предыдущее.
func (m *Manager) StartDevice(ctx context.Context, role string) (*DeviceStart, error) {
	scopes, ok := Scopes[role]
	if !ok {
		return nil, ErrBadRole
	}
	id, secret, err := m.app()
	if err != nil {
		return nil, err
	}
	if id == "" {
		return nil, ErrNotConfigured
	}
	if secret != "" {
		return nil, ErrUseAuthLink
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.idBase+"/oauth2/device",
		strings.NewReader(url.Values{"client_id": {id}, "scopes": {strings.Join(scopes, " ")}}.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var r struct {
		DeviceCode      string `json:"device_code"`
		UserCode        string `json:"user_code"`
		VerificationURI string `json:"verification_uri"`
		ExpiresIn       int    `json:"expires_in"`
		Interval        int    `json:"interval"`
	}
	if err := m.doJSON(req, &r); err != nil {
		return nil, err
	}
	if r.DeviceCode == "" || r.VerificationURI == "" {
		return nil, errors.New("twitch: empty device code response")
	}
	if r.ExpiresIn <= 0 || r.ExpiresIn > 3600 {
		r.ExpiresIn = 1800
	}
	role = strings.Clone(role)
	flowID := randHex(12)
	pctx, cancel := context.WithTimeout(context.Background(), time.Duration(r.ExpiresIn)*time.Second)
	f := &deviceFlow{state: DeviceState{Role: role, Status: DevicePending}, cancel: cancel, expires: m.now().Add(time.Duration(r.ExpiresIn) * time.Second)}

	m.dev.mu.Lock()
	if m.dev.flows == nil {
		m.dev.flows = map[string]*deviceFlow{}
	}
	now := m.now()
	for k, old := range m.dev.flows {
		if old.state.Role == role && old.state.Status == DevicePending {
			old.cancel()
			delete(m.dev.flows, k)
		} else if now.After(old.expires.Add(stateTTL)) {
			delete(m.dev.flows, k)
		}
	}
	m.dev.flows[flowID] = f
	m.dev.mu.Unlock()

	go m.pollDevice(pctx, flowID, f, id, role, r.DeviceCode, scopes, time.Duration(r.Interval)*time.Second)
	return &DeviceStart{Flow: flowID, UserCode: r.UserCode, VerificationURI: r.VerificationURI, ExpiresIn: r.ExpiresIn}, nil
}

// DeviceStatus — состояние подключения по его идентификатору.
func (m *Manager) DeviceStatus(flow string) (DeviceState, bool) {
	m.dev.mu.Lock()
	defer m.dev.mu.Unlock()
	f, ok := m.dev.flows[flow]
	if !ok {
		return DeviceState{}, false
	}
	return f.state, true
}

func (m *Manager) setDevice(f *deviceFlow, st DeviceState) {
	m.dev.mu.Lock()
	if f.state.Status == DevicePending { // отменённое подключение не оживает
		f.state = st
	}
	m.dev.mu.Unlock()
}

func (m *Manager) pollDevice(ctx context.Context, flowID string, f *deviceFlow, clientID, role, deviceCode string, scopes []string, interval time.Duration) {
	defer f.cancel()
	fail := func(msg string) { m.setDevice(f, DeviceState{Role: role, Status: DeviceFailed, Error: msg}) }
	wait := func() bool {
		d := interval
		if d < m.pollMin {
			d = m.pollMin
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(d):
			return true
		}
	}
	for wait() {
		tok, err := m.tokenRequest(ctx, url.Values{
			"client_id": {clientID}, "scopes": {strings.Join(scopes, " ")}, "device_code": {deviceCode},
			"grant_type": {"urn:ietf:params:oauth:grant-type:device_code"},
		})
		if err != nil {
			var he *httpError
			if errors.As(err, &he) {
				switch he.message {
				case "authorization_pending":
					continue
				case "slow_down":
					interval += 5 * time.Second
					continue
				}
				fail(deviceMessage(he.message))
				return
			}
			continue // сеть моргнула: пробуем ещё раз до истечения кода
		}
		v, err := m.validate(ctx, tok.AccessToken)
		if err != nil {
			fail(err.Error())
			return
		}
		acc := Account{
			Role: role, UserID: v.UserID, Login: v.Login, DisplayName: v.Login, Scopes: v.Scopes, ConnectedAt: m.now(),
			AccessToken: tok.AccessToken, RefreshToken: tok.RefreshToken,
			ExpiresAt: m.now().Add(time.Duration(tok.ExpiresIn) * time.Second),
		}
		if err := m.st.SaveAccount(acc); err != nil {
			fail(err.Error())
			return
		}
		m.setDevice(f, DeviceState{Role: role, Status: DeviceDone, Login: v.Login})
		if m.onConnect != nil {
			m.onConnect(acc)
		}
		return
	}
	fail("the code expired or was cancelled — start the connection again")
}

func deviceMessage(msg string) string {
	switch msg {
	case "access_denied":
		return "access was denied on Twitch"
	case "invalid device code", "expired_token":
		return "the code expired — start the connection again"
	}
	return msg
}
