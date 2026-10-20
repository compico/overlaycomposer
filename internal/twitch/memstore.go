package twitch

import "sync"

// MemStorage — хранилище в памяти (для тестов).
type MemStorage struct {
	mu   sync.Mutex
	set  map[string]string
	accs map[string]Account
}

func NewMemStorage() *MemStorage {
	return &MemStorage{set: map[string]string{}, accs: map[string]Account{}}
}

func (m *MemStorage) Setting(k string) (string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.set[k]
	return v, ok, nil
}

func (m *MemStorage) SetSetting(k, v string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.set[k] = v
	return nil
}

func (m *MemStorage) ListAccounts() ([]Account, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var o []Account
	for _, a := range m.accs {
		o = append(o, a)
	}
	return o, nil
}

func (m *MemStorage) GetAccount(r string) (*Account, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.accs[r]
	if !ok {
		return nil, nil
	}
	return &a, nil
}

func (m *MemStorage) SaveAccount(a Account) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.accs[a.Role] = a
	return nil
}

func (m *MemStorage) DeleteAccount(r string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.accs, r)
	return nil
}
