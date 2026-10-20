package store

import (
	"errors"
	"time"

	"gorm.io/gorm"
)

// DeviceRow — устройство, прошедшее сопряжение (доступ извне localhost). Токен не хранится: он подписан секретом
// приложения, а «живым» считается, пока строка устройства есть в этой таблице (удаление = отзыв).
type DeviceRow struct {
	ID        string `gorm:"primaryKey"`
	Name      string
	CreatedAt time.Time
	LastSeen  time.Time
	LastIP    string
}

func (DeviceRow) TableName() string { return "devices" }

func (s *Store) ListDevices() ([]DeviceRow, error) {
	var rows []DeviceRow
	err := s.db.Order("created_at").Find(&rows).Error
	return rows, err
}

func (s *Store) SaveDevice(d DeviceRow) error { return s.db.Save(&d).Error }

// DeleteDevice возвращает false, если такого устройства нет.
func (s *Store) DeleteDevice(id string) (bool, error) {
	res := s.db.Delete(&DeviceRow{}, "id = ?", id)
	return res.RowsAffected > 0, res.Error
}

// TouchDevice обновляет «последний раз виден».
func (s *Store) TouchDevice(id, ip string, at time.Time) error {
	return s.db.Model(&DeviceRow{}).Where("id = ?", id).Updates(map[string]any{"last_seen": at, "last_ip": ip}).Error
}

// Setting / SetSetting — произвольные строковые настройки (секрет подписи токенов и т.п.). "" и false — нет значения.
func (s *Store) Setting(key string) (string, bool, error) {
	var st settingRow
	err := s.db.First(&st, "key = ?", key).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return st.Value, true, nil
}

func (s *Store) SetSetting(key, value string) error {
	return s.db.Save(&settingRow{Key: key, Value: value}).Error
}
