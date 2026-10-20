package auth

import (
	"regexp"
	"strings"
	"testing"
	"time"
)

type memStore struct {
	settings map[string]string
	devs     map[string]Device
}

func newMem() *memStore { return &memStore{settings: map[string]string{}, devs: map[string]Device{}} }
func (m *memStore) Setting(k string) (string, bool, error) {
	v, ok := m.settings[k]
	return v, ok, nil
}
func (m *memStore) SetSetting(k, v string) error { m.settings[k] = v; return nil }
func (m *memStore) ListDevices() ([]Device, error) {
	var o []Device
	for _, d := range m.devs {
		o = append(o, d)
	}
	return o, nil
}
func (m *memStore) SaveDevice(d Device) error { m.devs[d.ID] = d; return nil }
func (m *memStore) DeleteDevice(id string) (bool, error) {
	_, ok := m.devs[id]
	delete(m.devs, id)
	return ok, nil
}
func (m *memStore) TouchDevice(id, ip string, at time.Time) error { return nil }

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func setup(t *testing.T) (*Manager, *memStore, *clock) {
	st, ck := newMem(), &clock{time.Unix(1_700_000_000, 0)}
	m, err := newManager(st, ck.now)
	if err != nil {
		t.Fatal(err)
	}
	return m, st, ck
}

func TestCodeFormatAndRotation(t *testing.T) {
	m, _, ck := setup(t)
	re := regexp.MustCompile(`^[A-Z]{6}$`)
	c1, exp := m.Pairing()
	if !re.MatchString(c1) || exp != CodeTTL {
		t.Fatalf("code %q exp %v", c1, exp)
	}
	if c, _ := m.Pairing(); c != c1 {
		t.Fatal("code changed within a minute")
	}
	ck.t = ck.t.Add(CodeTTL + time.Second)
	c2, _ := m.Pairing()
	if c2 == c1 {
		t.Fatal("code not rotated")
	}
}

func TestPairVerifyRevoke(t *testing.T) {
	m, st, _ := setup(t)
	code, _ := m.Pairing()
	tok, dev, err := m.Pair(strings.ToLower(code), "iPad", "10.0.0.5")
	if err != nil {
		t.Fatal(err)
	}
	if len(st.devs) != 1 || strings.Count(tok, ".") != 2 {
		t.Fatal("device or token not created")
	}
	if got, err := m.Verify(tok, "10.0.0.5"); err != nil || got.ID != dev.ID {
		t.Fatalf("verify: %v", err)
	}
	// код одноразовый
	if _, _, err := m.Pair(code, "x", "10.0.0.6"); err != ErrBadCode {
		t.Fatalf("code reuse: %v", err)
	}
	// подделка и порча
	if _, err := m.Verify(tok+"x", ""); err == nil {
		t.Fatal("tampered signature accepted")
	}
	parts := strings.Split(tok, ".")
	if _, err := m.Verify(parts[0]+"."+parts[1]+"A."+parts[2], ""); err == nil {
		t.Fatal("tampered payload accepted")
	}
	// новый Manager над той же базой принимает старый токен (секрет сохранён)
	m2, _ := newManager(st, time.Now)
	if _, err := m2.Verify(tok, ""); err != nil {
		t.Fatalf("token after restart: %v", err)
	}
	// отзыв
	if ok, err := m.Revoke(dev.ID); !ok || err != nil {
		t.Fatal("revoke")
	}
	if _, err := m.Verify(tok, ""); err == nil {
		t.Fatal("revoked token accepted")
	}
}

func TestPreviousCodeGrace(t *testing.T) {
	m, _, ck := setup(t)
	old, _ := m.Pairing()
	ck.t = ck.t.Add(CodeTTL + time.Second)
	if _, _, err := m.Pair(old, "d", "1.1.1.1"); err != nil {
		t.Fatalf("old code within grace: %v", err)
	}
	m2, _, ck2 := setup(t)
	old2, _ := m2.Pairing()
	ck2.t = ck2.t.Add(CodeTTL + codeGrace + time.Second)
	if _, _, err := m2.Pair(old2, "d", "1.1.1.1"); err != ErrBadCode {
		t.Fatalf("old code after grace: %v", err)
	}
}

func TestRateLimit(t *testing.T) {
	m, _, ck := setup(t)
	good, _ := m.Pairing()
	for i := 0; i < maxIPFails; i++ {
		if _, _, err := m.Pair("AAAAAA", "", "9.9.9.9"); err != ErrBadCode {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	// после серии неудач даже верный код с этого адреса не принимается
	if _, _, err := m.Pair(good, "", "9.9.9.9"); err != ErrLocked {
		t.Fatalf("locked: %v", err)
	}
	// другой адрес не затронут
	if _, _, err := m.Pair(good, "", "8.8.8.8"); err != nil {
		t.Fatalf("other ip: %v", err)
	}
	ck.t = ck.t.Add(ipLockout + time.Second)
	c, _ := m.Pairing()
	if _, _, err := m.Pair(c, "", "9.9.9.9"); err != nil {
		t.Fatalf("after lockout: %v", err)
	}
}

func TestGlobalLimit(t *testing.T) {
	m, _, _ := setup(t)
	for i := 0; i < maxGlobal; i++ {
		_, _, _ = m.Pair("AAAAAA", "", "10.1.0."+string(rune('a'+i%26))+string(rune('a'+i/26)))
	}
	code, _ := m.Pairing()
	if _, _, err := m.Pair(code, "", "7.7.7.7"); err != ErrLocked {
		t.Fatalf("global lock: %v", err)
	}
}
