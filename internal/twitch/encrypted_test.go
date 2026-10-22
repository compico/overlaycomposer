package twitch

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/compico/overlaycomposer/internal/secret"
)

func testBox(t *testing.T, b byte) *secret.Box {
	t.Helper()
	x, err := secret.NewBox(bytes.Repeat([]byte{b}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return x
}

func acc(role string) Account {
	return Account{Role: role, UserID: "1", Login: role, AccessToken: "access-" + role, RefreshToken: "refresh-" + role, ExpiresAt: time.Now().Add(time.Hour), ConnectedAt: time.Now()}
}

func TestEncryptedStorageHidesSecrets(t *testing.T) {
	raw := NewMemStorage()
	st := Encrypted(raw, testBox(t, 1))
	if err := st.SetSetting(keyClientSecret, "the-client-secret"); err != nil {
		t.Fatal(err)
	}
	st.SetSetting(keyClientID, "client-id")
	if err := st.SaveAccount(acc(RoleStreamer)); err != nil {
		t.Fatal(err)
	}
	// В «базе» открытого текста нет, Client ID (не секрет) остался как был.
	rawSecret, _, _ := raw.Setting(keyClientSecret)
	rawAcc, _ := raw.GetAccount(RoleStreamer)
	for _, v := range []string{rawSecret, rawAcc.AccessToken, rawAcc.RefreshToken} {
		if !secret.IsSealed(v) || strings.Contains(v, "access-") || strings.Contains(v, "refresh-") || strings.Contains(v, "the-client") {
			t.Fatalf("plaintext leaked: %q", v)
		}
	}
	if id, _, _ := raw.Setting(keyClientID); id != "client-id" {
		t.Fatal("client id must stay readable")
	}
	// Через обёртку всё читается как обычно.
	if v, ok, err := st.Setting(keyClientSecret); err != nil || !ok || v != "the-client-secret" {
		t.Fatalf("secret: %q %v %v", v, ok, err)
	}
	a, _ := st.GetAccount(RoleStreamer)
	if a.AccessToken != "access-streamer" || a.RefreshToken != "refresh-streamer" || a.LastError != "" {
		t.Fatalf("account: %+v", a)
	}
	list, _ := st.ListAccounts()
	if len(list) != 1 || list[0].AccessToken != "access-streamer" {
		t.Fatalf("list: %+v", list)
	}
	// Пустой секрет — «нет секрета».
	st.SetSetting(keyClientSecret, "")
	if v, _, _ := st.Setting(keyClientSecret); v != "" {
		t.Fatal("empty secret")
	}
}

func TestEncryptedStorageTokensAreBoundToTheirField(t *testing.T) {
	raw := NewMemStorage()
	st := Encrypted(raw, testBox(t, 1))
	st.SaveAccount(acc(RoleStreamer))
	st.SaveAccount(acc(RoleBot))
	// Подмена: токен бота записан в аккаунт стримера.
	s, _ := raw.GetAccount(RoleStreamer)
	b, _ := raw.GetAccount(RoleBot)
	s.AccessToken = b.AccessToken
	raw.SaveAccount(*s)
	got, _ := st.GetAccount(RoleStreamer)
	if got.AccessToken != "" || got.LastError == "" {
		t.Fatalf("swapped token must not be accepted: %+v", got)
	}
}

func TestLostKeyIsNotFatal(t *testing.T) {
	raw := NewMemStorage()
	Encrypted(raw, testBox(t, 1)).SetSetting(keyClientSecret, "s")
	Encrypted(raw, testBox(t, 1)).SaveAccount(acc(RoleBot))
	st := Encrypted(raw, testBox(t, 2)) // ключ заменили
	if v, ok, err := st.Setting(keyClientSecret); err != nil || ok || v != "" {
		t.Fatalf("secret with wrong key: %q %v %v", v, ok, err)
	}
	a, err := st.GetAccount(RoleBot)
	if err != nil || a == nil || a.AccessToken != "" || a.RefreshToken != "" || !strings.Contains(a.LastError, "Connect the account again") || a.Login != "bot" {
		t.Fatalf("account with wrong key: %+v %v", a, err)
	}
}

func TestSealLegacy(t *testing.T) {
	raw := NewMemStorage()
	raw.SetSetting(keyClientSecret, "old-secret")
	raw.SaveAccount(acc(RoleStreamer)) // открытым текстом, как до шифрования
	box := testBox(t, 1)
	if err := SealLegacy(raw, box); err != nil {
		t.Fatal(err)
	}
	if v, _, _ := raw.Setting(keyClientSecret); !secret.IsSealed(v) {
		t.Fatalf("secret not sealed: %q", v)
	}
	a, _ := raw.GetAccount(RoleStreamer)
	if !secret.IsSealed(a.AccessToken) || !secret.IsSealed(a.RefreshToken) {
		t.Fatalf("tokens not sealed: %+v", a)
	}
	st := Encrypted(raw, box)
	if v, _, _ := st.Setting(keyClientSecret); v != "old-secret" {
		t.Fatalf("migrated secret: %q", v)
	}
	if g, _ := st.GetAccount(RoleStreamer); g.AccessToken != "access-streamer" {
		t.Fatalf("migrated token: %+v", g)
	}
	// Повторный вызов ничего не меняет (значения не шифруются дважды).
	before, _ := raw.GetAccount(RoleStreamer)
	if err := SealLegacy(raw, box); err != nil {
		t.Fatal(err)
	}
	after, _ := raw.GetAccount(RoleStreamer)
	if before.AccessToken != after.AccessToken {
		t.Fatal("second run must be a no-op")
	}
}

func TestManagerWorksThroughEncryptedStorage(t *testing.T) {
	raw := NewMemStorage()
	m := New(Encrypted(raw, testBox(t, 1)))
	if err := m.SetApp("my-client-id", "my-secret"); err != nil {
		t.Fatal(err)
	}
	id, has, err := m.AppInfo()
	if err != nil || id != "my-client-id" || !has {
		t.Fatalf("app info: %q %v %v", id, has, err)
	}
	if v, _, _ := raw.Setting(keyClientSecret); strings.Contains(v, "my-secret") {
		t.Fatal("secret stored in plaintext")
	}
}
