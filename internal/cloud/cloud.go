// Package cloud — хранилища для файлов бандла настроек (#98, #198). Провайдер умеет только положить и забрать файл:
// что внутри файла и как его применять, знает internal/bundle.
package cloud

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"time"
)

// MaxFileSize — предел размера файла у провайдера (как у чтения бандла).
const MaxFileSize = 6 << 20

var (
	// ErrConflict — в хранилище лежит не та версия файла, которую ожидал вызывающий (см. Provider.Put).
	ErrConflict = errors.New("the file in the storage has changed")
	// ErrNotFound — такого файла нет.
	ErrNotFound = errors.New("file not found")
	// ErrNotConnected — провайдер не подключён.
	ErrNotConnected = errors.New("the storage is not connected")
)

// Remote — файл бандла в хранилище.
type Remote struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Size        int64     `json:"size"`
	ModTime     time.Time `json:"modTime"`
	Fingerprint string    `json:"fingerprint"` // sha256 содержимого: по нему видно, что файл изменили
}

// Provider — хранилище файлов. Реализации не знают про формат бандла и не решают, что перезаписывать.
type Provider interface {
	Name() string
	// Connected — готов ли провайдер к работе (авторизован, папка доступна).
	Connected() bool
	// Connect подключает провайдер (авторизация или проверка папки); Disconnect забывает подключение, файлы в хранилище не трогает.
	Connect(ctx context.Context) error
	Disconnect() error
	// List — файлы бандлов, новые первыми.
	List(ctx context.Context) ([]Remote, error)
	Get(ctx context.Context, id string) ([]byte, error)
	// Put кладёт файл name. expect — отпечаток версии, которую вызывающий собирается заменить: пусто — файла быть не должно.
	// Если в хранилище лежит другая версия, возвращается ErrConflict и ничего не меняется.
	Put(ctx context.Context, name string, data []byte, expect string) (Remote, error)
	Delete(ctx context.Context, id string) error
}

// Fingerprint — отпечаток содержимого файла.
func Fingerprint(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

var nameRe = regexp.MustCompile(`^oc-settings-[A-Za-z0-9._-]{1,100}\.json$`)

// ValidName — допустимое имя файла бандла (без путей и неожиданных символов): `oc-settings-<что угодно безопасное>.json`.
func ValidName(name string) error {
	if !nameRe.MatchString(name) {
		return fmt.Errorf("invalid file name %q: use oc-settings-<name>.json with letters, digits, . _ -", name)
	}
	return nil
}
