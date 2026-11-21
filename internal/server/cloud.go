package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"

	"github.com/compico/overlaycomposer/internal/cloud"
)

// Облако для файлов настроек (internal/cloud, #98/#198/#199): сейчас — обычная папка (в том числе синхронизируемая Dropbox, OneDrive,
// Syncthing). Файл настроек собирает и применяет тот же сервис, что и ручной экспорт/импорт (settings.go); здесь он только лежит и
// забирается. Только с этого компьютера. Ничего не делается само: кнопки «Upload / Download» в редакторе.

const cloudFolderKey = "cloud_folder"

func (s *Server) cloudRoutes() {
	app := s.app
	app.Get("/api/cloud", s.localOnly, s.cloudStatus)
	app.Put("/api/cloud", s.localOnly, s.appGuard, s.cloudConnect)
	app.Delete("/api/cloud", s.localOnly, s.appGuard, s.cloudDisconnect)
	app.Get("/api/cloud/files", s.localOnly, s.cloudList)
	app.Post("/api/cloud/upload", s.localOnly, s.appGuard, s.cloudUpload)
	app.Get("/api/cloud/files/:id", s.localOnly, s.cloudDownload)
	app.Delete("/api/cloud/files/:id", s.localOnly, s.appGuard, s.cloudDelete)
}

// cloudProvider — подключённый провайдер; сохранённая папка поднимается при первом обращении. nil — облако не настроено.
func (s *Server) cloudProvider() cloud.Provider {
	s.cloudMu.Lock()
	defer s.cloudMu.Unlock()
	if !s.cloudLoaded {
		s.cloudLoaded = true
		if s.d.Store != nil {
			if dir, ok, err := s.d.Store.Setting(cloudFolderKey); err == nil && ok && dir != "" {
				f := cloud.NewFolder(dir)
				if err := f.Connect(context.Background()); err != nil {
					slog.Warn("облако: папка недоступна", "dir", dir, "err", err)
				}
				s.cloudProv, s.cloudDir = f, dir
			}
		}
	}
	return s.cloudProv
}

// cloudErr переводит ошибки провайдера в ответ API.
func cloudErr(c fiber.Ctx, err error) error {
	switch {
	case errors.Is(err, cloud.ErrConflict):
		return c.Status(fiber.StatusConflict).JSON(fiber.Map{"error": "the file in the storage was changed by someone else", "code": "conflict"})
	case errors.Is(err, cloud.ErrNotFound):
		return notFound(c)
	default:
		return bad(c, err.Error())
	}
}

func (s *Server) cloudStatus(c fiber.Ctx) error {
	p := s.cloudProvider()
	if p == nil {
		return c.JSON(fiber.Map{"provider": "", "connected": false})
	}
	s.cloudMu.Lock()
	dir := s.cloudDir
	s.cloudMu.Unlock()
	return c.JSON(fiber.Map{"provider": p.Name(), "folder": dir, "connected": p.Connected()})
}

// PUT /api/cloud {folder} — подключить папку (создаётся, если её нет) и запомнить.
func (s *Server) cloudConnect(c fiber.Ctx) error {
	var b struct {
		Folder string `json:"folder"`
	}
	if !body(c, &b) {
		return bad(c, "invalid json")
	}
	dir := strings.TrimSpace(b.Folder)
	f := cloud.NewFolder(dir)
	if err := f.Connect(c.Context()); err != nil {
		return bad(c, err.Error())
	}
	if s.d.Store != nil {
		if err := s.d.Store.SetSetting(cloudFolderKey, dir); err != nil {
			return serverErr(c, err)
		}
	}
	s.cloudMu.Lock()
	s.cloudProv, s.cloudDir, s.cloudLoaded = f, dir, true
	s.cloudMu.Unlock()
	return s.cloudStatus(c)
}

// DELETE /api/cloud — забыть папку; файлы в ней остаются.
func (s *Server) cloudDisconnect(c fiber.Ctx) error {
	if p := s.cloudProvider(); p != nil {
		_ = p.Disconnect()
	}
	if s.d.Store != nil {
		if err := s.d.Store.SetSetting(cloudFolderKey, ""); err != nil {
			return serverErr(c, err)
		}
	}
	s.cloudMu.Lock()
	s.cloudProv, s.cloudDir, s.cloudLoaded = nil, "", true
	s.cloudMu.Unlock()
	return c.JSON(fiber.Map{"provider": "", "connected": false})
}

// connectedProvider — провайдер или ответ «не подключено».
func (s *Server) connectedProvider(c fiber.Ctx) (cloud.Provider, bool) {
	p := s.cloudProvider()
	if p == nil || !p.Connected() {
		_ = bad(c, cloud.ErrNotConnected.Error())
		return nil, false
	}
	return p, true
}

func (s *Server) cloudList(c fiber.Ctx) error {
	p, ok := s.connectedProvider(c)
	if !ok {
		return nil
	}
	list, err := p.List(c.Context())
	if err != nil {
		return cloudErr(c, err)
	}
	return c.JSON(fiber.Map{"files": list})
}

var unsafeNameChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// cloudFileName — имя по умолчанию: oc-settings-<компьютер>-<время>.json.
func cloudFileName(host string, now time.Time) string {
	host = strings.Trim(unsafeNameChars.ReplaceAllString(host, "-"), "-._")
	if host == "" {
		host = "pc"
	}
	if len(host) > 40 {
		host = host[:40]
	}
	return "oc-settings-" + host + "-" + now.Format("20060102-150405") + ".json"
}

// POST /api/cloud/upload {name?, sections?, expect?} — собрать настройки и положить файлом. Имя пусто — новый файл с именем по умолчанию.
// Заменить существующий файл можно, только указав его отпечаток (expect): иначе 409.
func (s *Server) cloudUpload(c fiber.Ctx) error {
	p, ok := s.connectedProvider(c)
	if !ok {
		return nil
	}
	var b struct {
		Name     string   `json:"name"`
		Sections []string `json:"sections"`
		Expect   string   `json:"expect"`
	}
	if len(c.Body()) > 0 && !body(c, &b) {
		return bad(c, "invalid json")
	}
	name := b.Name
	if name == "" {
		host, _ := os.Hostname()
		name = cloudFileName(host, time.Now())
	}
	f, err := s.settings().Export(c.Context(), b.Sections)
	if err != nil {
		return bad(c, err.Error())
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return serverErr(c, err)
	}
	r, err := p.Put(c.Context(), name, data, b.Expect)
	if err != nil {
		return cloudErr(c, err)
	}
	slog.Info("облако: настройки загружены", "file", r.Name, "size", r.Size)
	return c.JSON(r)
}

// GET /api/cloud/files/:id — {id, text}: содержимое файла строкой, без разбора. Редактор отдаёт её в обычный импорт
// (/api/settings/import/plan) байт в байт: контрольные суммы секций считаются по исходному тексту.
func (s *Server) cloudDownload(c fiber.Ctx) error {
	p, ok := s.connectedProvider(c)
	if !ok {
		return nil
	}
	data, err := p.Get(c.Context(), c.Params("id"))
	if err != nil {
		return cloudErr(c, err)
	}
	return c.JSON(fiber.Map{"id": c.Params("id"), "text": string(data)})
}

func (s *Server) cloudDelete(c fiber.Ctx) error {
	p, ok := s.connectedProvider(c)
	if !ok {
		return nil
	}
	if err := p.Delete(c.Context(), c.Params("id")); err != nil {
		return cloudErr(c, err)
	}
	return c.JSON(fiber.Map{"ok": true})
}
