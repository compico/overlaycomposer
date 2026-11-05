package store

import (
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// TrackRequestRow — заявка зрителя на трек (internal/trackreq). RedemptionID уникален: повторная доставка
// одного погашения Twitch не создаёт вторую заявку.
type TrackRequestRow struct {
	ID           int64  `gorm:"primaryKey;autoIncrement"`
	RedemptionID string `gorm:"uniqueIndex;not null"`
	RewardID     string
	UserLogin    string
	UserName     string
	UserInput    string
	TrackID      string
	AlbumID      string
	Status       string `gorm:"index"`
	Error        string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

func (TrackRequestRow) TableName() string { return "track_requests" }

// AddTrackRequest сохраняет заявку. created=false — заявка с таким RedemptionID уже есть (возвращается она).
func (s *Store) AddTrackRequest(r TrackRequestRow) (TrackRequestRow, bool, error) {
	r.ID = 0
	res := s.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&r)
	if res.Error != nil {
		return TrackRequestRow{}, false, res.Error
	}
	if res.RowsAffected > 0 {
		return r, true, nil
	}
	var old TrackRequestRow
	if err := s.db.First(&old, "redemption_id = ?", r.RedemptionID).Error; err != nil {
		return TrackRequestRow{}, false, err
	}
	return old, false, nil
}

func (s *Store) GetTrackRequest(id int64) (*TrackRequestRow, error) {
	var r TrackRequestRow
	err := s.db.First(&r, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// ListTrackRequests — последние заявки, новые первыми.
func (s *Store) ListTrackRequests(limit int) ([]TrackRequestRow, error) {
	var rows []TrackRequestRow
	err := s.db.Order("id DESC").Limit(limit).Find(&rows).Error
	return rows, err
}

// FirstTrackRequestWithStatus — самая ранняя заявка в статусе (nil — нет такой).
func (s *Store) FirstTrackRequestWithStatus(status string) (*TrackRequestRow, error) {
	var rows []TrackRequestRow
	if err := s.db.Where("status = ?", status).Order("id ASC").Limit(1).Find(&rows).Error; err != nil || len(rows) == 0 {
		return nil, err
	}
	return &rows[0], nil
}

func (s *Store) SetTrackRequestStatus(id int64, status, errText string) error {
	return s.db.Model(&TrackRequestRow{}).Where("id = ?", id).Updates(map[string]any{"status": status, "error": errText, "updated_at": time.Now()}).Error
}
