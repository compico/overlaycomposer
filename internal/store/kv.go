package store

import (
	"time"

	"gorm.io/gorm"

	"github.com/compico/overlaycomposer/internal/kv"
)

// kvRow — данные плагинов (internal/kv). Ключи только ASCII, поэтому сравнение префикса по substr безопасно.
type kvRow struct {
	Plugin    string `gorm:"primaryKey"`
	Key       string `gorm:"primaryKey"`
	Value     string
	Rev       int64
	UpdatedAt time.Time
}

func (kvRow) TableName() string { return "plugin_kv" }

// KV возвращает хранилище данных плагинов поверх этой базы.
func (s *Store) KV() kv.Backend { return kvBackend{s.db} }

type kvBackend struct{ db *gorm.DB }

// Update выполняет fn в транзакции (соединение в базе одно, поэтому транзакции и так идут по очереди).
func (b kvBackend) Update(fn func(kv.Tx) error) error {
	return b.db.Transaction(func(tx *gorm.DB) error { return fn(kvTx{tx}) })
}

type kvTx struct{ tx *gorm.DB }

func (t kvTx) Get(plugin, key string) (*kv.Row, error) {
	var rows []kvRow
	if err := t.tx.Where(`plugin = ? AND "key" = ?`, plugin, key).Limit(1).Find(&rows).Error; err != nil || len(rows) == 0 {
		return nil, err
	}
	return toKV(rows[0]), nil
}

func toKV(r kvRow) *kv.Row {
	return &kv.Row{Plugin: r.Plugin, Key: r.Key, Value: r.Value, Rev: r.Rev, UpdatedAt: r.UpdatedAt}
}

func (t kvTx) Put(r kv.Row) error {
	return t.tx.Save(&kvRow{Plugin: r.Plugin, Key: r.Key, Value: r.Value, Rev: r.Rev, UpdatedAt: r.UpdatedAt}).Error
}

func (t kvTx) Delete(plugin, key string) (bool, error) {
	res := t.tx.Where(`plugin = ? AND "key" = ?`, plugin, key).Delete(&kvRow{})
	return res.RowsAffected > 0, res.Error
}

func (t kvTx) List(plugin, prefix string, limit int) ([]kv.Row, error) {
	var rows []kvRow
	q := t.tx.Where("plugin = ?", plugin)
	if prefix != "" {
		q = q.Where(`substr("key", 1, ?) = ?`, len(prefix), prefix)
	}
	if err := q.Order(`"key"`).Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]kv.Row, len(rows))
	for i, r := range rows {
		out[i] = *toKV(r)
	}
	return out, nil
}

func (t kvTx) Stats(plugin string) (keys, bytes int64, err error) {
	err = t.tx.Raw(`SELECT COUNT(*), COALESCE(SUM(LENGTH(CAST(value AS BLOB))), 0) FROM plugin_kv WHERE plugin = ?`, plugin).Row().Scan(&keys, &bytes)
	return
}

func (t kvTx) Clear(plugin string) error {
	return t.tx.Where("plugin = ?", plugin).Delete(&kvRow{}).Error
}
