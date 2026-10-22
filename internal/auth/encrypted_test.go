package auth

import (
	"bytes"
	"testing"
	"time"

	"github.com/compico/overlaycomposer/internal/secret"
)

func box(t *testing.T, b byte) *secret.Box {
	t.Helper()
	x, err := secret.NewBox(bytes.Repeat([]byte{b}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return x
}

func TestEncryptedSecretAndDeviceTokensSurviveRestart(t *testing.T) {
	raw, ck := newMem(), &clock{time.Unix(1_700_000_000, 0)}
	m1, err := newManager(Encrypted(raw, box(t, 1)), ck.now)
	if err != nil {
		t.Fatal(err)
	}
	if v := raw.settings[secretKey]; !secret.IsSealed(v) {
		t.Fatalf("auth secret stored in plaintext: %q", v)
	}
	code, _ := m1.Pairing()
	token, _, err := m1.Pair(code, "phone", "10.0.0.5")
	if err != nil {
		t.Fatal(err)
	}
	// «Перезапуск» с тем же ключом: токен действует.
	m2, err := newManager(Encrypted(raw, box(t, 1)), ck.now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m2.Verify(token, "10.0.0.5"); err != nil {
		t.Fatalf("token must survive restart: %v", err)
	}
	// Ключ потерян: секрет не читается, создаётся новый, старые токены недействительны, приложение живо.
	m3, err := newManager(Encrypted(raw, box(t, 2)), ck.now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m3.Verify(token, "10.0.0.5"); err == nil {
		t.Fatal("old token must not be valid after the secret was regenerated")
	}
}

func TestSealLegacySecret(t *testing.T) {
	raw, ck := newMem(), &clock{time.Unix(1_700_000_000, 0)}
	m1, _ := newManager(raw, ck.now) // без шифрования, как раньше
	if secret.IsSealed(raw.settings[secretKey]) {
		t.Fatal("precondition")
	}
	code, _ := m1.Pairing()
	token, _, _ := m1.Pair(code, "tablet", "10.0.0.6")
	b := box(t, 1)
	if err := SealLegacy(raw, b); err != nil {
		t.Fatal(err)
	}
	if v := raw.settings[secretKey]; !secret.IsSealed(v) {
		t.Fatalf("not sealed: %q", v)
	}
	// Тот же секрет — прежние токены продолжают действовать.
	m2, err := newManager(Encrypted(raw, b), ck.now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m2.Verify(token, "10.0.0.6"); err != nil {
		t.Fatalf("migration must keep issued tokens valid: %v", err)
	}
	before := raw.settings[secretKey]
	SealLegacy(raw, b)
	if raw.settings[secretKey] != before {
		t.Fatal("second run must be a no-op")
	}
}
