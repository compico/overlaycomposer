package auth

import (
	"time"

	"github.com/compico/overlaycomposer/internal/store"
)

type dbStorage struct{ *store.Store }

// FromStore подключает Manager к базе приложения.
func FromStore(s *store.Store) Storage { return dbStorage{s} }

func (d dbStorage) ListDevices() ([]Device, error) {
	rows, err := d.Store.ListDevices()
	if err != nil {
		return nil, err
	}
	out := make([]Device, len(rows))
	for i, r := range rows {
		out[i] = Device{ID: r.ID, Name: r.Name, CreatedAt: r.CreatedAt, LastSeen: r.LastSeen, LastIP: r.LastIP}
	}
	return out, nil
}

func (d dbStorage) SaveDevice(v Device) error {
	return d.Store.SaveDevice(store.DeviceRow{ID: v.ID, Name: v.Name, CreatedAt: v.CreatedAt, LastSeen: v.LastSeen, LastIP: v.LastIP})
}

func (d dbStorage) TouchDevice(id, ip string, at time.Time) error {
	return d.Store.TouchDevice(id, ip, at)
}
