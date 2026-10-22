// Package secret — шифрование секретов «в покое» (токены Twitch, Client Secret, секрет подписи токенов устройств,
// токен GitHub): значение в базе/файле — не открытый текст, а `enc:v1:<base64(nonce|AES-256-GCM)>`.
//
// Ключ — случайные 32 байта в отдельном файле (secret.key рядом с config.json, а не в папке данных с базой), права 0600.
// На Windows файл ключа дополнительно защищён DPAPI (привязан к пользователю Windows).
//
// Что это даёт, а что нет — честно: защищает от утечки самой базы/папки данных (копия на бэкап, архив для поддержки, скриншот
// файла) и от случайного показа в логах; не защищает от вредоносной программы, работающей под тем же пользователем и
// способной прочитать и ключ, и базу (на Linux/macOS), или вызвать DPAPI от его имени (Windows).
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const prefix = "enc:v1:"

// ErrBadKeyFile — файл ключа повреждён. Молча создавать новый ключ нельзя: все сохранённые секреты стали бы нечитаемыми.
var ErrBadKeyFile = errors.New("secret key file is damaged")

// ErrCannotOpen — значение не расшифровалось (ключ другой или данные изменены).
var ErrCannotOpen = errors.New("cannot decrypt the value (the secret key changed or the data was modified)")

// Box шифрует и расшифровывает значения одним ключом.
type Box struct{ aead cipher.AEAD }

// NewBox — Box с готовым 32-байтным ключом (тесты; в приложении — OpenOrCreate).
func NewBox(key []byte) (*Box, error) {
	if len(key) != 32 {
		return nil, errors.New("secret: key must be 32 bytes")
	}
	blk, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(blk)
	if err != nil {
		return nil, err
	}
	return &Box{aead: g}, nil
}

// IsSealed — значение уже зашифровано этим форматом.
func IsSealed(s string) bool { return strings.HasPrefix(s, prefix) }

// Seal шифрует plain. aad привязывает значение к месту хранения (например, "twitch:streamer:refresh"): подменить зашифрованное
// значение одного поля значением другого не получится. Пустая строка остаётся пустой (нечего прятать, и «нет значения» не меняется).
func (b *Box) Seal(plain, aad string) string {
	if plain == "" {
		return ""
	}
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		panic(err) // ОС не дала случайных байт — безопасно продолжать нельзя
	}
	out := b.aead.Seal(nonce, nonce, []byte(plain), []byte(aad))
	return prefix + base64.RawStdEncoding.EncodeToString(out)
}

// Open расшифровывает значение. Строка без префикса enc:v1: — старое значение в открытом виде (до включения шифрования):
// возвращается как есть, legacy=true (вызывающий может перезаписать его уже зашифрованным).
func (b *Box) Open(s, aad string) (plain string, legacy bool, err error) {
	if s == "" {
		return "", false, nil
	}
	if !IsSealed(s) {
		return s, true, nil
	}
	raw, err := base64.RawStdEncoding.DecodeString(s[len(prefix):])
	if err != nil || len(raw) < b.aead.NonceSize()+b.aead.Overhead() {
		return "", false, ErrCannotOpen
	}
	n := b.aead.NonceSize()
	p, err := b.aead.Open(nil, raw[:n], raw[n:], []byte(aad))
	if err != nil {
		return "", false, ErrCannotOpen
	}
	return string(p), false, nil
}

const (
	keyHeaderRaw   = "OC-SECRET-KEY v1 raw\n"
	keyHeaderDPAPI = "OC-SECRET-KEY v1 dpapi\n"
)

// OpenOrCreate читает ключ из файла или создаёт новый (первый запуск). Повреждённый файл — ошибка, а не тихая замена.
func OpenOrCreate(file string) (*Box, error) {
	raw, err := os.ReadFile(file)
	switch {
	case err == nil:
		key, rewrite, err := decodeKeyFile(raw)
		if err != nil {
			return nil, fmt.Errorf("%w: %s (удалите файл, если ключ потерян: подключённые аккаунты Twitch придётся подключить заново)", err, file)
		}
		if rewrite { // на Windows ключ в открытом виде → защищаем DPAPI
			_ = writeKeyFile(file, key, true)
		}
		return NewBox(key)
	case errors.Is(err, fs.ErrNotExist):
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
		if err := writeKeyFile(file, key, false); err != nil {
			return nil, err
		}
		return NewBox(key)
	default:
		return nil, err
	}
}

func decodeKeyFile(raw []byte) (key []byte, rewrite bool, err error) {
	s := string(raw)
	var body string
	var dp bool
	switch {
	case strings.HasPrefix(s, keyHeaderRaw):
		body = s[len(keyHeaderRaw):]
	case strings.HasPrefix(s, keyHeaderDPAPI):
		body, dp = s[len(keyHeaderDPAPI):], true
	default:
		return nil, false, ErrBadKeyFile
	}
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(body))
	if err != nil {
		return nil, false, ErrBadKeyFile
	}
	if dp {
		if b, err = unprotect(b); err != nil {
			return nil, false, fmt.Errorf("%w: %v", ErrBadKeyFile, err)
		}
	}
	if len(b) != 32 {
		return nil, false, ErrBadKeyFile
	}
	return b, !dp && canProtect, nil
}

func writeKeyFile(file string, key []byte, overwrite bool) error {
	header, body := keyHeaderRaw, key
	if canProtect {
		p, err := protect(key)
		if err != nil {
			return fmt.Errorf("secret: DPAPI: %w", err)
		}
		header, body = keyHeaderDPAPI, p
	}
	data := []byte(header + base64.StdEncoding.EncodeToString(body) + "\n")
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return err
	}
	flags := os.O_WRONLY | os.O_CREATE | os.O_EXCL
	if overwrite {
		flags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	}
	f, err := os.OpenFile(file, flags, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
