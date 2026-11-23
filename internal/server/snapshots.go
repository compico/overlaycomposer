package server

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"path/filepath"
	"strconv"
	"time"

	"github.com/gofiber/fiber/v3"

	"github.com/compico/overlaycomposer/internal/bundle"
)

// Локальные снимки настроек (#228, часть #98): автоснимок при закрытии приложения, ручной снимок, бэкап перед импортом.
// Файлы лежат в <data>/backups, их не больше backup_max (по умолчанию 30); старые удаляются при записи нового.

const (
	backupAutoKey    = "backup_auto"     // "0" — автоснимок выключен; по умолчанию включён
	backupMaxKey     = "backup_max"      // максимум файлов
	backupSigKey     = "backup_last_sig" // отпечаток настроек в последнем снимке: без изменений новый автоснимок не пишется
	defaultBackupMax = 30
	maxBackupMax     = 200
)

// kvStore — настройки приложения в БД (*store.Store); в тестах подменяется.
type kvStore interface {
	Setting(key string) (string, bool, error)
	SetSetting(key, value string) error
}

func (s *Server) kv() kvStore {
	if s.kvImpl != nil {
		return s.kvImpl
	}
	if s.d.Store == nil { // иначе в интерфейсе оказался бы типизированный nil
		return nil
	}
	return s.d.Store
}

func (s *Server) backupDir() string { return filepath.Join(s.d.Paths.DataDir, "backups") }

// backupConf — автоснимок включён ли и максимум файлов (значения по умолчанию, если не заданы или повреждены).
func (s *Server) backupConf() (auto bool, max int) {
	auto, max = true, defaultBackupMax
	kv := s.kv()
	if kv == nil {
		return
	}
	if v, ok, _ := kv.Setting(backupAutoKey); ok && v == "0" {
		auto = false
	}
	if v, ok, _ := kv.Setting(backupMaxKey); ok {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= maxBackupMax {
			max = n
		}
	}
	return
}

// writeSnapshot сохраняет текущие настройки (все секции, включая необязательные: снимок остаётся на этом компьютере).
func (s *Server) writeSnapshot(ctx context.Context, kind string) (string, error) {
	f, err := s.settings().ExportAll(ctx)
	if err != nil {
		return "", err
	}
	_, max := s.backupConf()
	path, err := bundle.WriteBackup(s.backupDir(), f, time.Now(), max, kind)
	if err != nil {
		return "", err
	}
	if kv := s.kv(); kv != nil {
		_ = kv.SetSetting(backupSigKey, bundle.Signature(f))
	}
	return path, nil
}

// backupSettings — бэкап перед импортом (bundle.Service.BeforeApply).
func (s *Server) backupSettings(ctx context.Context) error {
	path, err := s.writeSnapshot(ctx, bundle.KindImport)
	if err == nil {
		slog.Info("настройки: снимок перед импортом", "file", path)
	}
	return err
}

// AutoSnapshot вызывается при закрытии приложения: если автоснимок включён и настройки изменились с прошлого снимка — пишет файл.
func (s *Server) AutoSnapshot(ctx context.Context) {
	auto, max := s.backupConf()
	if !auto || s.kv() == nil {
		return
	}
	f, err := s.settings().ExportAll(ctx)
	if err != nil {
		slog.Warn("настройки: автоснимок не собран", "err", err)
		return
	}
	last, _, _ := s.kv().Setting(backupSigKey)
	sig, wrote, err := bundle.Autosave(s.backupDir(), f, last, time.Now(), max)
	if err != nil {
		slog.Warn("настройки: автоснимок не записан", "err", err)
		return
	}
	if wrote {
		_ = s.kv().SetSetting(backupSigKey, sig)
		slog.Info("настройки: автоснимок при закрытии записан")
	}
}

func (s *Server) snapshotRoutes() {
	app := s.app
	app.Get("/api/settings/backup", s.localOnly, s.backupInfo)
	app.Put("/api/settings/backup", s.localOnly, s.appGuard, s.backupPut)
	app.Post("/api/settings/backup/snapshot", s.localOnly, s.appGuard, s.backupSnapshot)
	app.Get("/api/settings/backup/files/:name", s.localOnly, s.backupFile)
	app.Delete("/api/settings/backup/files/:name", s.localOnly, s.appGuard, s.backupDelete)
}

func (s *Server) backupInfo(c fiber.Ctx) error {
	auto, max := s.backupConf()
	list, err := bundle.ListBackups(s.backupDir())
	if err != nil {
		return serverErr(c, err)
	}
	return c.JSON(fiber.Map{"auto": auto, "max": max, "files": list})
}

// PUT /api/settings/backup {auto?, max?} — автоснимок при закрытии и максимум файлов.
func (s *Server) backupPut(c fiber.Ctx) error {
	var b struct {
		Auto *bool `json:"auto"`
		Max  *int  `json:"max"`
	}
	if !body(c, &b) {
		return bad(c, "invalid json")
	}
	if b.Max != nil && (*b.Max < 1 || *b.Max > maxBackupMax) {
		return bad(c, "max must be between 1 and "+strconv.Itoa(maxBackupMax))
	}
	kv := s.kv()
	if kv == nil {
		return bad(c, "settings storage is not available")
	}
	if b.Auto != nil {
		v := "1"
		if !*b.Auto {
			v = "0"
		}
		if err := kv.SetSetting(backupAutoKey, v); err != nil {
			return serverErr(c, err)
		}
	}
	if b.Max != nil {
		if err := kv.SetSetting(backupMaxKey, strconv.Itoa(*b.Max)); err != nil {
			return serverErr(c, err)
		}
	}
	return s.backupInfo(c)
}

// POST /api/settings/backup/snapshot — снимок вручную.
func (s *Server) backupSnapshot(c fiber.Ctx) error {
	if _, err := s.writeSnapshot(c.Context(), bundle.KindManual); err != nil {
		return serverErr(c, err)
	}
	return s.backupInfo(c)
}

// GET /api/settings/backup/files/:name — {name, text}: исходный текст снимка; редактор отдаёт его в обычный импорт.
func (s *Server) backupFile(c fiber.Ctx) error {
	name := c.Params("name")
	data, err := bundle.ReadBackup(s.backupDir(), name)
	if errors.Is(err, fs.ErrNotExist) {
		return notFound(c)
	}
	if err != nil {
		return serverErr(c, err)
	}
	return c.JSON(fiber.Map{"name": name, "text": string(data)})
}

func (s *Server) backupDelete(c fiber.Ctx) error {
	err := bundle.DeleteBackup(s.backupDir(), c.Params("name"))
	if errors.Is(err, fs.ErrNotExist) {
		return notFound(c)
	}
	if err != nil {
		return serverErr(c, err)
	}
	return s.backupInfo(c)
}
