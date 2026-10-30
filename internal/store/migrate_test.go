package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func rawDB(t *testing.T, path string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

func exists(path string) bool { _, err := os.Stat(path); return err == nil }

func TestFreshDatabaseGetsLatestVersionWithoutBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oc.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	v, _ := userVersion(s.db)
	if v != migrations[len(migrations)-1].version {
		t.Fatalf("user_version = %d", v)
	}
	if err := s.Seed(); err != nil {
		t.Fatal(err)
	}
	if exists(path + ".bak-v0") {
		t.Fatal("копия не нужна: данных ещё не было")
	}
}

// База, созданная приложением до появления версий схемы (user_version = 0), с данными: данные целы, копия есть.
func TestLegacyDatabaseIsAdoptedAndBackedUp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oc.db")
	db := rawDB(t, path)
	if err := db.AutoMigrate(&sourceRow{}, &settingRow{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&sourceRow{ID: "old", Name: "Legacy widget", Type: "iframe", URL: "https://x.example"}).Error; err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	_ = sqlDB.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.GetSource("old")
	if err != nil || got == nil || got.Name != "Legacy widget" {
		t.Fatalf("данные потерялись: %+v %v", got, err)
	}
	if v, _ := userVersion(s.db); v != 1 {
		t.Fatalf("user_version = %d", v)
	}
	bak := rawDB(t, path+".bak-v0")
	var n int64
	if err := bak.Model(&sourceRow{}).Count(&n).Error; err != nil || n != 1 {
		t.Fatalf("копия пуста: %d %v", n, err)
	}
}

func TestSecondOpenDoesNotMigrateOrBackupAgain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oc.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if exists(path+".bak-v0") || exists(path+".bak-v1") {
		t.Fatal("повторный запуск не должен делать копий")
	}
}

// sad path: база от более новой версии приложения не открывается и не изменяется.
func TestNewerSchemaIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oc.db")
	db := rawDB(t, path)
	if err := db.Exec("PRAGMA user_version = 99").Error; err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	_ = sqlDB.Close()

	_, err := Open(path)
	var ne *NewerSchemaError
	if !errors.As(err, &ne) || ne.Have != 99 {
		t.Fatalf("err = %v", err)
	}
	db = rawDB(t, path)
	if v, _ := userVersion(db); v != 99 {
		t.Fatalf("версия изменилась: %d", v)
	}
	if db.Migrator().HasTable(&sourceRow{}) {
		t.Fatal("схема новой базы не должна трогаться")
	}
}

// sad path: сбой миграции откатывает и её изменения, и версию; копия базы остаётся.
func TestFailedMigrationRollsBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oc.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Seed(); err != nil {
		t.Fatal(err)
	}
	s.Close()

	db := rawDB(t, path)
	boom := errors.New("boom")
	list := append(append([]migration{}, migrations...), migration{2, "ломается", func(tx *gorm.DB) error {
		if err := tx.Exec("CREATE TABLE half_done (id INTEGER)").Error; err != nil {
			return err
		}
		return boom
	}})
	err = migrate(db, path, list)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if v, _ := userVersion(db); v != 1 {
		t.Fatalf("версия после отката = %d", v)
	}
	if db.Migrator().HasTable("half_done") {
		t.Fatal("изменения неудавшейся миграции не откатились")
	}
	if !exists(path + ".bak-v1") {
		t.Fatal("копия перед миграцией должна остаться")
	}
}

func TestLaterMigrationAppliesInOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oc.db")
	db := rawDB(t, path)
	var order []int
	list := []migration{
		{1, "a", func(tx *gorm.DB) error { order = append(order, 1); return tx.AutoMigrate(&settingRow{}, &sourceRow{}) }},
		{2, "b", func(tx *gorm.DB) error {
			order = append(order, 2)
			return tx.Exec("INSERT INTO settings(key, value) VALUES ('migrated', 'yes')").Error
		}},
	}
	if err := migrate(db, path, list); err != nil {
		t.Fatal(err)
	}
	if len(order) != 2 || order[0] != 1 || order[1] != 2 {
		t.Fatalf("order = %v", order)
	}
	// повторный запуск ничего не применяет
	order = nil
	if err := migrate(db, path, list); err != nil || len(order) != 0 {
		t.Fatalf("повтор: %v %v", err, order)
	}
	var v string
	if err := db.Raw("SELECT value FROM settings WHERE key='migrated'").Row().Scan(&v); err != nil || v != "yes" {
		t.Fatalf("%q %v", v, err)
	}
}
