package auth

import (
	"log/slog"

	"github.com/compico/overlaycomposer/internal/secret"
)

// Секрет подписи токенов устройств (auth_secret) хранится зашифрованным: по утёкшей базе нельзя выпустить токен
// «сопряжённого» устройства. Если секрет не расшифровался (потерян secret.key), Manager создаст новый — ранее выданные
// токены перестанут действовать, устройства придётся сопрячь заново.

type encrypted struct {
	Storage
	box *secret.Box
}

// Encrypted оборачивает хранилище шифрованием секрета подписи.
func Encrypted(st Storage, box *secret.Box) Storage { return encrypted{st, box} }

const secretAAD = "auth:secret"

func (e encrypted) Setting(key string) (string, bool, error) {
	v, ok, err := e.Storage.Setting(key)
	if err != nil || !ok || key != secretKey {
		return v, ok, err
	}
	plain, _, err := e.box.Open(v, secretAAD)
	if err != nil {
		slog.Warn("auth: секрет токенов не расшифровался, будет создан новый (устройства нужно сопрячь заново)", "err", err)
		return "", false, nil
	}
	return plain, true, nil
}

func (e encrypted) SetSetting(key, value string) error {
	if key == secretKey {
		value = e.box.Seal(value, secretAAD)
	}
	return e.Storage.SetSetting(key, value)
}

// SealLegacy шифрует секрет, сохранённый до включения шифрования (вызывается на «сырой» базе при запуске).
func SealLegacy(raw Storage, box *secret.Box) error {
	v, ok, err := raw.Setting(secretKey)
	if err != nil || !ok || v == "" || secret.IsSealed(v) {
		return err
	}
	return raw.SetSetting(secretKey, box.Seal(v, secretAAD))
}
