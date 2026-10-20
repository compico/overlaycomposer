// Package auth — доступ к приложению с других устройств. С 127.0.0.1 доступ открыт; извне нужен токен (JWT HS256),
// который выдаётся в обмен на одноразовый код из 6 заглавных латинских букв. Код меняется раз в минуту и сгорает
// после использования. Токен бессрочный, но «жив», только пока устройство есть в списке (отзыв = удаление).
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"
)

const (
	CodeLen      = 6
	CodeTTL      = time.Minute
	codeGrace    = 10 * time.Second // старый код ещё принимается столько секунд после смены (человек уже вводил)
	maxIPFails   = 5
	ipLockout    = 5 * time.Minute
	maxGlobal    = 30 // неудачных попыток в минуту со всех адресов вместе
	touchEvery   = 5 * time.Minute
	secretKey    = "auth_secret"
	jwtHeaderB64 = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9" // {"alg":"HS256","typ":"JWT"}
)

var (
	ErrBadCode     = errors.New("invalid code")
	ErrLocked      = errors.New("too many attempts, try later")
	ErrInvalidAuth = errors.New("invalid token")
)

// Device — сопряжённое устройство.
type Device struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
	LastSeen  time.Time `json:"lastSeen"`
	LastIP    string    `json:"lastIp"`
}

// Storage — то, что нужно от базы.
type Storage interface {
	Setting(key string) (string, bool, error)
	SetSetting(key, value string) error
	ListDevices() ([]Device, error)
	SaveDevice(Device) error
	DeleteDevice(id string) (bool, error)
	TouchDevice(id, ip string, at time.Time) error
}

type failState struct {
	n     int
	until time.Time
}

type Manager struct {
	st     Storage
	now    func() time.Time
	secret []byte

	mu      sync.Mutex
	code    string
	expires time.Time
	prev    string
	prevEnd time.Time
	devices map[string]*Device
	touched map[string]time.Time
	fails   map[string]*failState
	gWindow time.Time
	gFails  int
}

// New загружает (или создаёт) секрет и список устройств.
func New(st Storage) (*Manager, error) { return newManager(st, time.Now) }

func newManager(st Storage, now func() time.Time) (*Manager, error) {
	m := &Manager{st: st, now: now, devices: map[string]*Device{}, touched: map[string]time.Time{}, fails: map[string]*failState{}}
	v, ok, err := st.Setting(secretKey)
	if err != nil {
		return nil, err
	}
	if ok {
		m.secret, err = hex.DecodeString(v)
	}
	if !ok || err != nil || len(m.secret) < 32 {
		m.secret = randBytes(32)
		if err := st.SetSetting(secretKey, hex.EncodeToString(m.secret)); err != nil {
			return nil, err
		}
		// Старые токены подписаны другим секретом — устройства без действующих токенов бесполезны.
	}
	devs, err := st.ListDevices()
	if err != nil {
		return nil, err
	}
	for i := range devs {
		d := devs[i]
		m.devices[d.ID] = &d
	}
	return m, nil
}

func randBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand: " + err.Error())
	}
	return b
}

// newCode — 6 букв A–Z без смещения распределения (отбрасываем байты ≥ 234 = 26*9).
func newCode() string {
	out := make([]byte, 0, CodeLen)
	for len(out) < CodeLen {
		for _, b := range randBytes(16) {
			if b < 234 && len(out) < CodeLen {
				out = append(out, 'A'+b%26)
			}
		}
	}
	return string(out)
}

// rotate обновляет код, если он истёк. Вызывать под m.mu.
func (m *Manager) rotate() {
	now := m.now()
	if m.code != "" && now.Before(m.expires) {
		return
	}
	if m.code != "" {
		m.prev, m.prevEnd = m.code, m.expires.Add(codeGrace)
	}
	m.code = newCode()
	m.expires = now.Add(CodeTTL)
}

// Pairing возвращает текущий код и сколько он ещё действует.
func (m *Manager) Pairing() (code string, expiresIn time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rotate()
	return m.code, m.expires.Sub(m.now())
}

// Pair меняет код на токен нового устройства. ip — адрес клиента (для ограничения перебора).
func (m *Manager) Pair(code, name, ip string) (token string, dev Device, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	if f := m.fails[ip]; f != nil && now.Before(f.until) {
		return "", Device{}, ErrLocked
	}
	if now.Sub(m.gWindow) > time.Minute {
		m.gWindow, m.gFails = now, 0
	}
	if m.gFails >= maxGlobal {
		return "", Device{}, ErrLocked
	}
	m.rotate()
	code = strings.ToUpper(strings.TrimSpace(code))
	ok := subtle.ConstantTimeCompare([]byte(code), []byte(m.code)) == 1
	if !ok && m.prev != "" && now.Before(m.prevEnd) {
		ok = subtle.ConstantTimeCompare([]byte(code), []byte(m.prev)) == 1
	}
	if !ok {
		m.gFails++
		f := m.fails[ip]
		if f == nil {
			if len(m.fails) > 1000 { // не даём таблице расти бесконечно
				m.fails = map[string]*failState{}
			}
			f = &failState{}
			m.fails[ip] = f
		}
		f.n++
		if f.n >= maxIPFails {
			f.n, f.until = 0, now.Add(ipLockout)
		}
		return "", Device{}, ErrBadCode
	}
	delete(m.fails, ip)
	m.code, m.prev = "", "" // одноразовый: следующий код сгенерируется при обращении
	name = strings.TrimSpace(name)
	if name == "" {
		name = "Device"
	}
	if r := []rune(name); len(r) > 60 {
		name = string(r[:60])
	}
	dev = Device{ID: hex.EncodeToString(randBytes(8)), Name: name, CreatedAt: now, LastSeen: now, LastIP: ip}
	if err := m.st.SaveDevice(dev); err != nil {
		return "", Device{}, err
	}
	m.devices[dev.ID] = &dev
	return m.sign(dev.ID, now), dev, nil
}

type claims struct {
	Sub string `json:"sub"`
	Iat int64  `json:"iat"`
}

func (m *Manager) sign(sub string, now time.Time) string {
	pl, _ := json.Marshal(claims{Sub: sub, Iat: now.Unix()})
	body := jwtHeaderB64 + "." + base64.RawURLEncoding.EncodeToString(pl)
	return body + "." + m.mac(body)
}

func (m *Manager) mac(s string) string {
	h := hmac.New(sha256.New, m.secret)
	h.Write([]byte(s))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}

// Verify проверяет подпись токена и что устройство не отозвано. ip используется для отметки «последний раз виден».
func (m *Manager) Verify(token, ip string) (Device, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] != jwtHeaderB64 {
		return Device{}, ErrInvalidAuth
	}
	if !hmac.Equal([]byte(parts[2]), []byte(m.mac(parts[0]+"."+parts[1]))) {
		return Device{}, ErrInvalidAuth
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Device{}, ErrInvalidAuth
	}
	var cl claims
	if json.Unmarshal(raw, &cl) != nil {
		return Device{}, ErrInvalidAuth
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	d := m.devices[cl.Sub]
	if d == nil {
		return Device{}, ErrInvalidAuth
	}
	now := m.now()
	if now.Sub(m.touched[d.ID]) >= touchEvery {
		m.touched[d.ID] = now
		d.LastSeen, d.LastIP = now, ip
		_ = m.st.TouchDevice(d.ID, ip, now)
	}
	return *d, nil
}

// Devices — список сопряжённых устройств.
func (m *Manager) Devices() []Device {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Device, 0, len(m.devices))
	for _, d := range m.devices {
		out = append(out, *d)
	}
	for i := 1; i < len(out); i++ { // по дате создания, список короткий
		for j := i; j > 0 && out[j].CreatedAt.Before(out[j-1].CreatedAt); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// Revoke отзывает устройство; false — такого нет.
func (m *Manager) Revoke(id string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.devices[id]; !ok {
		return false, nil
	}
	if _, err := m.st.DeleteDevice(id); err != nil {
		return false, err
	}
	delete(m.devices, id)
	delete(m.touched, id)
	return true, nil
}
