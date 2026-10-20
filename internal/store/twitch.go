package store

import (
	"errors"
	"time"

	"gorm.io/gorm"
)

// TwitchAccountRow — подключённый аккаунт Twitch в одной из ролей ("streamer" | "bot").
// Токены лежат в базе открытым текстом (как и пароль OBS в config.json): файл доступен только пользователю ОС.
type TwitchAccountRow struct {
	Role         string `gorm:"primaryKey"`
	UserID       string
	Login        string
	DisplayName  string
	AccessToken  string
	RefreshToken string
	Scopes       string // через пробел
	ExpiresAt    time.Time
	ConnectedAt  time.Time
	LastError    string // последняя ошибка обновления токена; не пусто — нужно переподключить
}

func (TwitchAccountRow) TableName() string { return "twitch_accounts" }

func (s *Store) ListTwitchAccounts() ([]TwitchAccountRow, error) {
	var rows []TwitchAccountRow
	err := s.db.Order("role").Find(&rows).Error
	return rows, err
}

func (s *Store) GetTwitchAccount(role string) (*TwitchAccountRow, error) {
	var r TwitchAccountRow
	err := s.db.First(&r, "role = ?", role).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func (s *Store) SaveTwitchAccount(r TwitchAccountRow) error { return s.db.Save(&r).Error }

func (s *Store) DeleteTwitchAccount(role string) error {
	return s.db.Delete(&TwitchAccountRow{}, "role = ?", role).Error
}
