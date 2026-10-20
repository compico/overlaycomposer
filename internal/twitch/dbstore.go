package twitch

import (
	"strings"

	"github.com/compico/overlaycomposer/internal/store"
)

type dbStorage struct{ *store.Store }

// FromStore подключает Manager к базе приложения.
func FromStore(s *store.Store) Storage { return dbStorage{s} }

func toAccount(r store.TwitchAccountRow) Account {
	return Account{
		Role: r.Role, UserID: r.UserID, Login: r.Login, DisplayName: r.DisplayName,
		Scopes: strings.Fields(r.Scopes), ConnectedAt: r.ConnectedAt, LastError: r.LastError,
		AccessToken: r.AccessToken, RefreshToken: r.RefreshToken, ExpiresAt: r.ExpiresAt,
	}
}

func (d dbStorage) ListAccounts() ([]Account, error) {
	rows, err := d.Store.ListTwitchAccounts()
	if err != nil {
		return nil, err
	}
	out := make([]Account, len(rows))
	for i, r := range rows {
		out[i] = toAccount(r)
	}
	return out, nil
}

func (d dbStorage) GetAccount(role string) (*Account, error) {
	r, err := d.Store.GetTwitchAccount(role)
	if err != nil || r == nil {
		return nil, err
	}
	a := toAccount(*r)
	return &a, nil
}

func (d dbStorage) SaveAccount(a Account) error {
	return d.Store.SaveTwitchAccount(store.TwitchAccountRow{
		Role: a.Role, UserID: a.UserID, Login: a.Login, DisplayName: a.DisplayName,
		AccessToken: a.AccessToken, RefreshToken: a.RefreshToken, Scopes: strings.Join(a.Scopes, " "),
		ExpiresAt: a.ExpiresAt, ConnectedAt: a.ConnectedAt, LastError: a.LastError,
	})
}

func (d dbStorage) DeleteAccount(role string) error { return d.Store.DeleteTwitchAccount(role) }
