package secret

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func box(t *testing.T) *Box {
	t.Helper()
	b, err := NewBox(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestSealOpen(t *testing.T) {
	b := box(t)
	s := b.Seal("super-secret-token", "twitch:streamer:access")
	if !IsSealed(s) || strings.Contains(s, "super-secret") {
		t.Fatalf("not sealed: %q", s)
	}
	if s2 := b.Seal("super-secret-token", "twitch:streamer:access"); s2 == s {
		t.Fatal("nonce must differ between seals")
	}
	got, legacy, err := b.Open(s, "twitch:streamer:access")
	if err != nil || legacy || got != "super-secret-token" {
		t.Fatalf("open: %q %v %v", got, legacy, err)
	}
	// Значение, перенесённое в другое поле, не читается.
	if _, _, err := b.Open(s, "twitch:bot:access"); !errors.Is(err, ErrCannotOpen) {
		t.Fatalf("aad: %v", err)
	}
	// Подделка.
	bad := s[:len(s)-3] + "AAA"
	if _, _, err := b.Open(bad, "twitch:streamer:access"); !errors.Is(err, ErrCannotOpen) {
		t.Fatalf("tamper: %v", err)
	}
	for _, junk := range []string{"enc:v1:", "enc:v1:!!!", "enc:v1:AAAA"} {
		if _, _, err := b.Open(junk, "x"); !errors.Is(err, ErrCannotOpen) {
			t.Errorf("junk %q: %v", junk, err)
		}
	}
	// Другой ключ.
	other, _ := NewBox(bytes.Repeat([]byte{9}, 32))
	if _, _, err := other.Open(s, "twitch:streamer:access"); !errors.Is(err, ErrCannotOpen) {
		t.Fatalf("other key: %v", err)
	}
}

func TestEmptyAndLegacy(t *testing.T) {
	b := box(t)
	if b.Seal("", "x") != "" {
		t.Fatal("empty stays empty")
	}
	if v, legacy, err := b.Open("", "x"); v != "" || legacy || err != nil {
		t.Fatal("open empty")
	}
	v, legacy, err := b.Open("old-plaintext-token", "x")
	if err != nil || !legacy || v != "old-plaintext-token" {
		t.Fatalf("legacy: %q %v %v", v, legacy, err)
	}
}

func TestKeyFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "sub", "secret.key")
	b1, err := OpenOrCreate(file)
	if err != nil {
		t.Fatal(err)
	}
	sealed := b1.Seal("value", "ctx")
	if runtime.GOOS != "windows" {
		st, err := os.Stat(file)
		if err != nil || st.Mode().Perm() != 0o600 {
			t.Fatalf("key file mode: %v %v", st, err)
		}
	}
	raw, _ := os.ReadFile(file)
	if bytes.Contains(raw, bytes.Repeat([]byte{0}, 8)) { // не пустой ключ
		t.Fatal("suspicious key file")
	}
	// Перезапуск: тот же ключ.
	b2, err := OpenOrCreate(file)
	if err != nil {
		t.Fatal(err)
	}
	if v, _, err := b2.Open(sealed, "ctx"); err != nil || v != "value" {
		t.Fatalf("reopen: %q %v", v, err)
	}
	// Повреждённый файл — ошибка, а не новый ключ (иначе все секреты стали бы нечитаемыми).
	for _, junk := range []string{"garbage", keyHeaderRaw + "###", keyHeaderRaw + "AAAA", ""} {
		os.WriteFile(file, []byte(junk), 0o600)
		if _, err := OpenOrCreate(file); !errors.Is(err, ErrBadKeyFile) {
			t.Errorf("junk %q: %v", junk, err)
		}
	}
	if data, _ := os.ReadFile(file); string(data) != "" {
		t.Fatal("damaged file must not be overwritten")
	}
}

func TestNewBoxKeyLength(t *testing.T) {
	if _, err := NewBox(make([]byte, 16)); err == nil {
		t.Fatal("16-byte key must be rejected")
	}
}
