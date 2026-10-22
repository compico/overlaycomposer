package twitch

import (
	"log/slog"

	"github.com/compico/overlaycomposer/internal/secret"
)

// Обёртка над Storage, которая хранит секреты (Client Secret, access/refresh-токены аккаунтов) зашифрованными (secret.Box):
// остальной код Manager работает с открытыми значениями и ничего не знает о шифровании.
//
// Если значение не расшифровалось (потерян/заменён secret.key или база изменена) — это не авария: секрет считается
// отсутствующим, у аккаунта пропадают токены и появляется понятная ошибка «подключите заново».

const undecryptable = "Saved credentials cannot be read (the secret key changed). Connect the account again."

type encrypted struct {
	Storage
	box *secret.Box
}

// Encrypted оборачивает хранилище шифрованием секретов.
func Encrypted(st Storage, box *secret.Box) Storage { return encrypted{st, box} }

func aad(role, field string) string { return "twitch:" + role + ":" + field }

func (e encrypted) Setting(key string) (string, bool, error) {
	v, ok, err := e.Storage.Setting(key)
	if err != nil || !ok || key != keyClientSecret {
		return v, ok, err
	}
	plain, _, err := e.box.Open(v, "twitch:app:secret")
	if err != nil {
		slog.Warn("twitch: Client Secret не расшифровался, введите его заново", "err", err)
		return "", false, nil
	}
	return plain, ok, nil
}

func (e encrypted) SetSetting(key, value string) error {
	if key == keyClientSecret {
		value = e.box.Seal(value, "twitch:app:secret")
	}
	return e.Storage.SetSetting(key, value)
}

func (e encrypted) open(a *Account) {
	var err1, err2 error
	a.AccessToken, _, err1 = e.box.Open(a.AccessToken, aad(a.Role, "access"))
	a.RefreshToken, _, err2 = e.box.Open(a.RefreshToken, aad(a.Role, "refresh"))
	if err1 != nil || err2 != nil {
		a.AccessToken, a.RefreshToken = "", ""
		a.LastError = undecryptable
	}
}

func (e encrypted) ListAccounts() ([]Account, error) {
	list, err := e.Storage.ListAccounts()
	for i := range list {
		e.open(&list[i])
	}
	return list, err
}

func (e encrypted) GetAccount(role string) (*Account, error) {
	a, err := e.Storage.GetAccount(role)
	if err != nil || a == nil {
		return a, err
	}
	e.open(a)
	return a, nil
}

func (e encrypted) SaveAccount(a Account) error {
	a.AccessToken = e.box.Seal(a.AccessToken, aad(a.Role, "access"))
	a.RefreshToken = e.box.Seal(a.RefreshToken, aad(a.Role, "refresh"))
	return e.Storage.SaveAccount(a)
}

// SealLegacy шифрует значения, сохранённые до включения шифрования (открытым текстом). Вызывается при запуске на «сырой»
// базе (без обёртки Encrypted); уже зашифрованные значения не трогает.
func SealLegacy(raw Storage, box *secret.Box) error {
	if v, ok, err := raw.Setting(keyClientSecret); err != nil {
		return err
	} else if ok && v != "" && !secret.IsSealed(v) {
		if err := raw.SetSetting(keyClientSecret, box.Seal(v, "twitch:app:secret")); err != nil {
			return err
		}
	}
	list, err := raw.ListAccounts()
	if err != nil {
		return err
	}
	for _, a := range list {
		if (a.AccessToken == "" || secret.IsSealed(a.AccessToken)) && (a.RefreshToken == "" || secret.IsSealed(a.RefreshToken)) {
			continue
		}
		if !secret.IsSealed(a.AccessToken) {
			a.AccessToken = box.Seal(a.AccessToken, aad(a.Role, "access"))
		}
		if !secret.IsSealed(a.RefreshToken) {
			a.RefreshToken = box.Seal(a.RefreshToken, aad(a.Role, "refresh"))
		}
		if err := raw.SaveAccount(a); err != nil {
			return err
		}
	}
	return nil
}
