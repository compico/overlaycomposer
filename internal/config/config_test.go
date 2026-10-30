package config

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/compico/overlaycomposer/internal/secret"
)

func box(t *testing.T, fill byte) *secret.Box {
	t.Helper()
	b, err := secret.NewBox(bytes.Repeat([]byte{fill}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func raw(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestObsPasswordNeverOnDiskInPlaintext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UseBox(box(t, 1)); err != nil {
		t.Fatal(err)
	}
	pw := "hunter2-пароль"
	if _, _, err := s.Apply(Patch{Obs: &ObsPatch{Password: &pw}}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw(t, path), "hunter2") || !strings.Contains(raw(t, path), "enc:v1:") {
		t.Fatalf("пароль на диске не зашифрован:\n%s", raw(t, path))
	}
	if got := s.Get().Obs.Password; got != pw {
		t.Fatalf("в памяти пароль должен быть открытым: %q", got)
	}

	// после перезапуска пароль читается тем же ключом
	s2, _ := Load(path)
	if err := s2.UseBox(box(t, 1)); err != nil {
		t.Fatal(err)
	}
	if got := s2.Get().Obs.Password; got != pw {
		t.Fatalf("после перезапуска: %q", got)
	}
}

func TestLegacyPlaintextPasswordIsSealedOnStart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"obs":{"enabled":true,"url":"ws://127.0.0.1:4455","password":"old-plain"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s, _ := Load(path)
	if err := s.UseBox(box(t, 1)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw(t, path), "old-plain") {
		t.Fatalf("старый пароль остался открытым:\n%s", raw(t, path))
	}
	if s.Get().Obs.Password != "old-plain" {
		t.Fatal("пароль должен остаться рабочим")
	}
}

func TestObsPasswordSadPathWrongKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	s, _ := Load(path)
	_ = s.UseBox(box(t, 1))
	pw := "secret"
	if _, _, err := s.Apply(Patch{Obs: &ObsPatch{Password: &pw}}); err != nil {
		t.Fatal(err)
	}
	s2, _ := Load(path)
	if err := s2.UseBox(box(t, 2)); err != nil { // другой ключ — не авария
		t.Fatal(err)
	}
	if s2.Get().Obs.Password != "" || s2.Get().Public().HasObsPassword {
		t.Fatal("пароль, который не расшифровался, должен считаться не заданным (а не отдаваться шифртекстом OBS)")
	}
}

func TestPasswordUnchangedByOtherPatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	s, _ := Load(path)
	_ = s.UseBox(box(t, 1))
	pw := "keep"
	_, _, _ = s.Apply(Patch{Obs: &ObsPatch{Password: &pw}})
	port := 9001.0
	if _, _, err := s.Apply(Patch{Port: &port}); err != nil {
		t.Fatal(err)
	}
	s2, _ := Load(path)
	_ = s2.UseBox(box(t, 1))
	if s2.Get().Obs.Password != "keep" {
		t.Fatal("пароль потерялся при сохранении другой настройки")
	}
}
