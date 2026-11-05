package trackreq

import (
	"strconv"

	"github.com/compico/overlaycomposer/internal/store"
)

const (
	keyEnabled  = "trackreq_enabled"
	keyRewardID = "trackreq_reward_id"
)

type dbStore struct{ st *store.Store }

// FromStore — Store поверх базы приложения.
func FromStore(st *store.Store) Store { return dbStore{st} }

func toRequest(r store.TrackRequestRow) Request {
	return Request{
		ID: r.ID, RedemptionID: r.RedemptionID, RewardID: r.RewardID, UserLogin: r.UserLogin, UserName: r.UserName, Input: r.UserInput,
		TrackID: r.TrackID, AlbumID: r.AlbumID, Status: r.Status, Error: r.Error, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

func ptr(r *store.TrackRequestRow, err error) (*Request, error) {
	if err != nil || r == nil {
		return nil, err
	}
	q := toRequest(*r)
	return &q, nil
}

func (d dbStore) Add(r Request) (Request, bool, error) {
	row, created, err := d.st.AddTrackRequest(store.TrackRequestRow{
		RedemptionID: r.RedemptionID, RewardID: r.RewardID, UserLogin: r.UserLogin, UserName: r.UserName, UserInput: r.Input,
		TrackID: r.TrackID, AlbumID: r.AlbumID, Status: r.Status, Error: r.Error,
	})
	if err != nil {
		return Request{}, false, err
	}
	return toRequest(row), created, nil
}

func (d dbStore) Get(id int64) (*Request, error) { return ptr(d.st.GetTrackRequest(id)) }

func (d dbStore) List(limit int) ([]Request, error) {
	rows, err := d.st.ListTrackRequests(limit)
	out := make([]Request, 0, len(rows))
	for _, r := range rows {
		out = append(out, toRequest(r))
	}
	return out, err
}

func (d dbStore) NextQueued() (*Request, error) {
	return ptr(d.st.FirstTrackRequestWithStatus(StatusQueued))
}

func (d dbStore) Active() (*Request, error) { return ptr(d.st.FirstTrackRequestWithStatus(StatusSent)) }

func (d dbStore) SetStatus(id int64, status, errText string) error {
	return d.st.SetTrackRequestStatus(id, status, errText)
}

func (d dbStore) LoadConfig() (Config, error) {
	on, _, err := d.st.Setting(keyEnabled)
	if err != nil {
		return Config{}, err
	}
	id, _, err := d.st.Setting(keyRewardID)
	if err != nil {
		return Config{}, err
	}
	b, _ := strconv.ParseBool(on)
	return Config{Enabled: b, RewardID: id}, nil
}

func (d dbStore) SaveConfig(c Config) error {
	if err := d.st.SetSetting(keyEnabled, strconv.FormatBool(c.Enabled)); err != nil {
		return err
	}
	return d.st.SetSetting(keyRewardID, c.RewardID)
}
