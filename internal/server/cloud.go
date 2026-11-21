package server

import (
	"context"
	"encoding/json"
	"errors"
	"html"
	"log/slog"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"

	"github.com/compico/overlaycomposer/internal/cloud"
)

// Облако для файлов настроек (internal/cloud, #98/#198/#199): сейчас — обычная папка (в том числе синхронизируемая Dropbox, OneDrive,
// Syncthing). Файл настроек собирает и применяет тот же сервис, что и ручной экспорт/импорт (settings.go); здесь он только лежит и
// забирается. Только с этого компьютера. Ничего не делается само: кнопки «Upload / Download» в редакторе.

const (
	cloudFolderKey   = "cloud_folder"
	cloudProviderKey = "cloud_provider" // "folder" | "google"; пусто при сохранённой папке — folder (до появления Google Drive)
	driveTokenKey    = "cloud_google_refresh"
	driveTokenAAD    = "cloud:google:refresh"
)

func (s *Server) cloudRoutes() {
	app := s.app
	app.Get("/api/cloud", s.localOnly, s.cloudStatus)
	app.Put("/api/cloud", s.localOnly, s.appGuard, s.cloudConnect)
	app.Delete("/api/cloud", s.localOnly, s.appGuard, s.cloudDisconnect)
	app.Post("/api/cloud/google/start", s.localOnly, s.appGuard, s.googleStart)
	app.Get("/auth/google/callback", s.localOnly, s.googleCallback)
	app.Get("/auth/google/done", s.localOnly, s.googleDone)
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
			kind, _, _ := s.d.Store.Setting(cloudProviderKey)
			if kind == "google" && s.googleDrive() != nil {
				s.cloudProv = s.googleDrive()
			} else if dir, ok, err := s.d.Store.Setting(cloudFolderKey); err == nil && ok && dir != "" {
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
	google := fiber.Map{"available": s.googleDrive() != nil && s.googleDrive().Configured(), "connected": s.googleDrive() != nil && s.googleDrive().Connected()}
	if p == nil {
		return c.JSON(fiber.Map{"provider": "", "connected": false, "google": google})
	}
	s.cloudMu.Lock()
	dir := s.cloudDir
	s.cloudMu.Unlock()
	return c.JSON(fiber.Map{"provider": p.Name(), "folder": dir, "connected": p.Connected(), "google": google})
}

// driveTokens хранит refresh-токен Google в настройках БД, зашифрованным ключом приложения.
type driveTokens struct{ s *Server }

func (t driveTokens) Load() (string, error) {
	v, ok, err := t.s.d.Store.Setting(driveTokenKey)
	if err != nil || !ok || v == "" {
		return "", err
	}
	plain, _, err := t.s.d.Box.Open(v, driveTokenAAD)
	if err != nil { // ключ потерян или заменён: считаем, что подключения нет, подключиться можно заново
		slog.Warn("облако: токен Google не расшифровался, подключитесь заново", "err", err)
		return "", nil
	}
	return plain, nil
}

func (t driveTokens) Save(tok string) error {
	return t.s.d.Store.SetSetting(driveTokenKey, t.s.d.Box.Seal(tok, driveTokenAAD))
}

// googleDrive — провайдер Google Drive; nil, если нет базы или ключа шифрования. Создаётся один раз.
func (s *Server) googleDrive() *cloud.Drive {
	s.driveOnce.Do(func() {
		if s.d.Store != nil && s.d.Box != nil {
			s.drive = cloud.NewDrive(cloud.GoogleConfig{ClientID: cloud.GoogleClientID, ClientSecret: cloud.GoogleClientSecret}, driveTokens{s})
		}
	})
	return s.drive
}

// googleRedirectURI — loopback-адрес этого приложения (для клиента типа Desktop Google принимает любой порт).
func (s *Server) googleRedirectURI() string {
	return "http://127.0.0.1:" + strconv.Itoa(s.d.Running.Port) + "/auth/google/callback"
}

// POST /api/cloud/google/start — адрес страницы согласия Google; редактор открывает его в браузере.
func (s *Server) googleStart(c fiber.Ctx) error {
	d := s.googleDrive()
	if d == nil {
		return bad(c, cloud.ErrNotConfigured.Error())
	}
	u, err := d.AuthURL(s.googleRedirectURI())
	if err != nil {
		return cloudErr(c, err)
	}
	return c.JSON(fiber.Map{"url": u})
}

// GET /auth/google/callback — сюда Google возвращает пользователя после согласия. Код и state не должны оставаться в адресной
// строке и истории: ответ обрабатывается и сразу перенаправляется на чистый адрес /auth/google/done.
func (s *Server) googleCallback(c fiber.Ctx) error {
	c.Set("Referrer-Policy", "no-referrer")
	c.Set("Cache-Control", "no-store")
	done := func(status string) error {
		return c.Redirect().Status(fiber.StatusSeeOther).To("/auth/google/done?status=" + status)
	}
	d := s.googleDrive()
	if d == nil {
		return done("failed")
	}
	if e := c.Query("error"); e != "" {
		slog.Warn("облако: Google не дал доступ", "error", e)
		return done("denied")
	}
	state, code := strings.Clone(c.Query("state")), strings.Clone(c.Query("code")) // Fiber переиспользует буферы запроса
	if err := d.Complete(c.Context(), state, code, s.googleRedirectURI()); err != nil {
		slog.Warn("облако: подключение Google Drive не удалось", "err", err)
		return done("failed")
	}
	if err := s.d.Store.SetSetting(cloudProviderKey, "google"); err != nil {
		slog.Warn("облако: не сохранён выбранный провайдер", "err", err)
		return done("failed")
	}
	s.cloudMu.Lock()
	s.cloudProv, s.cloudDir, s.cloudLoaded = d, "", true
	s.cloudMu.Unlock()
	slog.Info("облако: Google Drive подключён")
	return done("ok")
}

// GET /auth/google/done — итог подключения; текст фиксированный (детали ошибки — в логе приложения).
func (s *Server) googleDone(c fiber.Ctx) error {
	title, msg, status := "Could not connect Google Drive", "Close this tab and try again in Overlay Composer. Details are in the app log.", fiber.StatusBadRequest
	switch c.Query("status") {
	case "ok":
		title, msg, status = "Google Drive connected", "You can close this tab and return to Overlay Composer.", fiber.StatusOK
	case "denied":
		title, msg = "Google Drive was not connected", "Access was not granted. You can close this tab."
	}
	return s.cloudResult(c, status, title, msg)
}

func (s *Server) cloudResult(c fiber.Ctx, status int, title, msg string) error {
	c.Status(status)
	c.Set("Cache-Control", "no-store")
	return s.htmlString(c, `<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Overlay Composer — Google Drive</title>
<body style="margin:0;background:#15171c;color:#e6e8ee;font-family:sans-serif;text-align:center;padding:60px 16px">
<h1 style="font-size:20px">`+html.EscapeString(title)+`</h1><p style="color:#9aa3b2">`+html.EscapeString(msg)+`</p></body>`)
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
		if err := s.d.Store.SetSetting(cloudProviderKey, "folder"); err != nil {
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
		// выбранная папка забывается; токен Google удаляет Disconnect провайдера (выше). Провайдер больше не выбран.
		for _, k := range []string{cloudFolderKey, cloudProviderKey} {
			if err := s.d.Store.SetSetting(k, ""); err != nil {
				return serverErr(c, err)
			}
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
