package store

import (
	"fmt"
	"log/slog"
	"os"
	"strings"

	"gorm.io/gorm"
)

// Версия схемы хранится в SQLite (PRAGMA user_version) — в самом файле базы, а не в таблице.
//
// Как это работает:
//   - миграции пронумерованы и применяются по порядку, каждая в своей транзакции вместе с записью новой версии:
//     сбой посередине откатывает и изменения, и версию;
//   - перед миграцией существующей базы с данными делается копия <база>.bak-v<версия> (VACUUM INTO — целостный снимок);
//   - базу, созданную более новой версией приложения, не открываем: старый код мог бы испортить схему, которую не знает.
//
// Как добавить изменение схемы: дописать migration в конец списка (версия = предыдущая + 1) и НЕ менять уже выпущенные.
// Миграции пишутся идемпотентно (Migrator().HasColumn/HasTable): на чистой базе первая миграция создаёт таблицы по
// актуальным структурам, и более поздняя «добавить колонку» увидит, что колонка уже есть.

type migration struct {
	version int
	name    string
	up      func(tx *gorm.DB) error
}

var migrations = []migration{
	{1, "исходная схема", func(tx *gorm.DB) error {
		// До появления версий схема создавалась AutoMigrate при каждом запуске — база без версии (0) с этими таблицами
		// считается уже находящейся в состоянии версии 1; AutoMigrate безопасно довозит недостающее.
		return tx.AutoMigrate(&sourceRow{}, &layoutRow{}, &sceneRow{}, &settingRow{}, &DeviceRow{}, &TwitchAccountRow{}, &kvRow{})
	}},
}

// NewerSchemaError — база создана более новой версией приложения.
type NewerSchemaError struct{ Have, Supported int }

func (e *NewerSchemaError) Error() string {
	return fmt.Sprintf("база данных создана более новой версией приложения (схема v%d, эта версия понимает до v%d): обновите приложение", e.Have, e.Supported)
}

func userVersion(db *gorm.DB) (int, error) {
	var v int
	if err := db.Raw("PRAGMA user_version").Row().Scan(&v); err != nil {
		return 0, err
	}
	return v, nil
}

func inMemory(path string) bool {
	return path == "" || strings.Contains(path, ":memory:") || strings.Contains(path, "mode=memory")
}

// backupBefore сохраняет копию базы перед миграцией. Старая копия той же версии заменяется.
func backupBefore(db *gorm.DB, path string, version int) (string, error) {
	if inMemory(path) {
		return "", nil
	}
	dst := fmt.Sprintf("%s.bak-v%d", path, version)
	if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
		return "", err
	}
	if err := db.Exec("VACUUM INTO ?", dst).Error; err != nil {
		return "", err
	}
	return dst, nil
}

// migrate приводит схему к последней версии из списка.
func migrate(db *gorm.DB, path string, list []migration) error {
	latest := 0
	if len(list) > 0 {
		latest = list[len(list)-1].version
	}
	cur, err := userVersion(db)
	if err != nil {
		return fmt.Errorf("чтение версии схемы: %w", err)
	}
	if cur > latest {
		return &NewerSchemaError{Have: cur, Supported: latest}
	}
	if cur == latest {
		return nil
	}
	// «Есть данные» = база уже создавалась (в том числе версией приложения без нумерации схемы).
	if db.Migrator().HasTable(&sourceRow{}) {
		dst, err := backupBefore(db, path, cur)
		if err != nil {
			return fmt.Errorf("не удалось сохранить копию базы перед обновлением схемы: %w", err)
		}
		if dst != "" {
			slog.Info("база: копия перед обновлением схемы", "file", dst, "from", cur, "to", latest)
		}
	}
	for _, m := range list {
		if m.version <= cur {
			continue
		}
		err := db.Transaction(func(tx *gorm.DB) error {
			if err := m.up(tx); err != nil {
				return err
			}
			return tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", m.version)).Error
		})
		if err != nil {
			return fmt.Errorf("миграция %d (%s): %w", m.version, m.name, err)
		}
		slog.Info("база: схема обновлена", "version", m.version, "name", m.name)
	}
	return nil
}
