// Overlay Composer — один бинарник: HTTP/WS-сервер, редактор, runtime для OBS, значок в трее.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/compico/overlaycomposer/internal/auth"
	"github.com/compico/overlaycomposer/internal/chat"
	"github.com/compico/overlaycomposer/internal/config"
	"github.com/compico/overlaycomposer/internal/hub"
	"github.com/compico/overlaycomposer/internal/kv"
	"github.com/compico/overlaycomposer/internal/logx"
	"github.com/compico/overlaycomposer/internal/market"
	"github.com/compico/overlaycomposer/internal/model"
	"github.com/compico/overlaycomposer/internal/notify"
	"github.com/compico/overlaycomposer/internal/obs"
	"github.com/compico/overlaycomposer/internal/paths"
	"github.com/compico/overlaycomposer/internal/platform"
	"github.com/compico/overlaycomposer/internal/plugins"
	"github.com/compico/overlaycomposer/internal/proxy"
	"github.com/compico/overlaycomposer/internal/secret"
	"github.com/compico/overlaycomposer/internal/server"
	"github.com/compico/overlaycomposer/internal/store"
	"github.com/compico/overlaycomposer/internal/tray"
	"github.com/compico/overlaycomposer/internal/twitch"
	"github.com/compico/overlaycomposer/internal/update"
)

// version подставляется при сборке: -ldflags "-X main.version=1.2.3"
var version = "dev"

// channel == "release" ставит только релизная сборка в CI (-X main.channel=release); только у неё работают обновления.
var channel = ""

func main() { os.Exit(run()) }

func run() int {
	var (
		home      = flag.String("home", "", "портативный режим: config.json, data/ и logs/ лежат в этом каталоге (или OVERLAY_HOME)")
		autostart = flag.Bool("autostart", false, "запуск из автозапуска системы: не открывать браузер")
		noOpen    = flag.Bool("no-open", false, "не открывать браузер при старте")
		noTray    = flag.Bool("no-tray", false, "без значка в трее (работает до Ctrl+C)")
		showVer   = flag.Bool("version", false, "показать версию и выйти")
	)
	flag.Parse()
	if *showVer {
		fmt.Println(paths.AppTitle, version)
		return 0
	}

	p, err := paths.Resolve(*home)
	if err != nil {
		platform.ErrorBox(paths.AppTitle, "Не удалось подготовить каталоги: "+err.Error())
		return 1
	}
	if err := logx.Init(p.LogFile, stderrIsTerminal()); err != nil {
		platform.ErrorBox(paths.AppTitle, "Не удалось открыть лог: "+err.Error())
		return 1
	}
	defer logx.Close()
	slog.Info("запуск", "version", version, "os", runtime.GOOS, "mode", p.Mode, "config", p.ConfigFile, "data", p.DataDir)

	cfgStore, err := config.Load(p.ConfigFile)
	if err != nil {
		slog.Error("config", "err", err)
		platform.ErrorBox(paths.AppTitle, "Не удалось прочитать/создать config.json: "+err.Error())
		return 1
	}
	cfg := cfgStore.Get()

	// Быстрая проверка «уже запущено»: если порт занят нашим приложением — просто открываем редактор.
	if ours(cfg.Host, cfg.Port) {
		slog.Info("приложение уже запущено — открываю редактор")
		if !*autostart {
			_ = platform.OpenTarget(editorURL(cfg))
		}
		return 0
	}

	st, err := openStore(p)
	if err != nil {
		slog.Error("база данных", "err", err)
		platform.ErrorBox(paths.AppTitle, "Не удалось открыть базу данных: "+err.Error())
		return 1
	}
	defer st.Close()

	var srv *server.Server
	var tr *tray.Tray

	// Проверка обновлений — только релизная сборка и если не выключено в config.json.
	var upd *update.Checker
	if update.Enabled(channel, version) && !cfg.NoUpdateCheck {
		upd = &update.Checker{Current: version, StateFile: filepath.Join(p.DataDir, "update.json")}
	}

	obsClient := obs.New(cfg.Obs.URL, cfg.Obs.Password)
	h := hub.New(st, obsClient, func() string { return srv.OverlayURL() }, func() bool { return cfgStore.Get().Obs.Enabled })
	obsClient.OnStatus(h.OnObsStatus)
	obsClient.OnSceneList(h.OnObsSceneList)
	obsClient.OnScene(h.OnObsScene)
	obsClient.OnSceneRenamed(h.OnObsSceneRenamed)

	px := proxy.New(func(id string) *model.Source {
		s, _ := st.GetSource(id)
		return s
	})
	px.SetDebug(cfg.DebugProxy)

	quit := make(chan struct{})
	var once sync.Once
	shutdown := func() {
		once.Do(func() {
			slog.Info("завершение работы")
			close(quit)
			if tr != nil {
				tr.Stop()
			}
		})
	}

	// Секреты (токены Twitch, Client Secret, секрет подписи токенов устройств, токен GitHub) лежат зашифрованными;
	// ключ — отдельный файл рядом с config.json (а не в папке данных с базой).
	box, err := secret.OpenOrCreate(filepath.Join(filepath.Dir(p.ConfigFile), "secret.key"))
	if err != nil {
		slog.Error("ключ шифрования", "err", err)
		platform.ErrorBox(paths.AppTitle, "Не удалось подготовить ключ шифрования секретов:\n"+err.Error())
		return 1
	}
	rawAuth, rawTw := auth.FromStore(st), twitch.FromStore(st)
	if err := auth.SealLegacy(rawAuth, box); err != nil { // значения, сохранённые открытым текстом до включения шифрования
		slog.Error("шифрование секрета авторизации", "err", err)
		platform.ErrorBox(paths.AppTitle, "Не удалось зашифровать сохранённые секреты: "+err.Error())
		return 1
	}
	if err := twitch.SealLegacy(rawTw, box); err != nil {
		slog.Error("шифрование токенов Twitch", "err", err)
		platform.ErrorBox(paths.AppTitle, "Не удалось зашифровать сохранённые секреты: "+err.Error())
		return 1
	}

	authMgr, err := auth.New(auth.Encrypted(rawAuth, box))
	if err != nil {
		slog.Error("авторизация устройств", "err", err)
		platform.ErrorBox(paths.AppTitle, "Не удалось подготовить авторизацию устройств: "+err.Error())
		return 1
	}

	twMgr := twitch.New(twitch.Encrypted(rawTw, box))
	chatSvc := chat.New(twMgr)
	defer chatSvc.Close()

	pluginReg := plugins.New(plugins.Builtin(), os.DirFS(p.PluginsDir))
	mk, err := market.New(market.Config{
		File: filepath.Join(p.DataDir, "market.json"), Dir: p.PluginsDir, Reg: pluginReg, Builtin: pluginReg.IsBuiltin, Box: box,
	})
	if err != nil {
		slog.Error("магазин плагинов", "err", err)
		platform.ErrorBox(paths.AppTitle, "Не удалось прочитать список магазина плагинов: "+err.Error())
		return 1
	}

	// Центр уведомлений: обновления плагинов (проверка при запуске и по кнопке) и новая версия приложения.
	center := notify.Open(filepath.Join(p.DataDir, "notifications.json"))
	checkPlugins := func(ctx context.Context, force bool) error {
		return center.Check(ctx, force, func(ctx context.Context) ([]notify.Candidate, bool) {
			ups, ok := mk.Updates(ctx, force)
			out := make([]notify.Candidate, 0, len(ups))
			for _, u := range ups {
				out = append(out, notify.Candidate{
					Key:   "plugin:" + u.ID + "@" + u.To,
					Title: fmt.Sprintf("%s: update %s", u.Name, u.To),
					Text:  fmt.Sprintf("Plugin %s %s → %s is available", u.Name, u.From, u.To),
					Data:  u,
				})
			}
			return out, ok
		})
	}

	srv = server.New(server.Deps{
		Paths: p, Cfg: cfgStore, Running: cfg, Store: st, Hub: h, Obs: obsClient, Proxy: px,
		Auth:         authMgr,
		Twitch:       twMgr,
		Chat:         chatSvc,
		KV:           kv.New(st.KV()),
		Plugins:      pluginReg,
		Market:       mk,
		Notify:       center,
		CheckPlugins: checkPlugins,
		App: server.App{
			OS:           runtime.GOOS,
			Autostart:    platform.Autostart,
			SetAutostart: func(on bool) error { err := platform.SetAutostart(on); syncTray(tr); return err },
			Open: func(what string) error {
				switch what {
				case "logs":
					return platform.OpenTarget(p.LogDir)
				case "plugins":
					return platform.OpenTarget(p.PluginsDir)
				}
				return platform.OpenTarget(p.DataDir)
			},
			Quit:          shutdown,
			TrayRunning:   func() bool { return tr != nil && tr.Running() },
			TrayAvailable: !*noTray,
			Version:       version,
			Update: func() any {
				if upd == nil {
					return nil
				}
				return upd.Info()
			},
		},
	})

	errc, err := srv.Listen()
	if err != nil {
		slog.Error("не удалось занять адрес", "host", cfg.Host, "port", cfg.Port, "err", err)
		platform.ErrorBox(paths.AppTitle, fmt.Sprintf("Не удалось запустить сервер на %s:%d.\n%v\n\nЕсли порт занят другой программой, измените port в %s и запустите снова.",
			cfg.Host, cfg.Port, err, p.ConfigFile))
		return 1
	}
	slog.Info("сервер запущен", "editor", srv.EditorURL(), "overlay", srv.OverlayURL())
	if cfg.Host != "127.0.0.1" && cfg.Host != "localhost" {
		slog.Warn("сервер доступен не только с этого компьютера, а авторизации в MVP нет")
	}
	if cfg.Obs.Enabled {
		slog.Info("obs-websocket", "url", cfg.Obs.URL)
		obsClient.Start()
	} else {
		slog.Info("obs-websocket выключен (сцену сообщает overlay из OBS)")
	}

	go func() {
		if err := <-errc; err != nil {
			slog.Error("сервер остановился", "err", err)
			platform.ErrorBox(paths.AppTitle, "Сервер остановился: "+err.Error())
			shutdown()
		}
	}()
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
		<-sig
		shutdown()
	}()

	openEditor := func() {
		if err := platform.OpenTarget(srv.EditorURL()); err != nil {
			slog.Warn("не удалось открыть браузер", "err", err)
		}
	}
	first := !*autostart && !*noOpen

	// Запрос версии — в самом конце запуска, когда всё остальное уже поднялось (при падении раньше его не будет),
	// и с задержкой; частоту ограничивает update.Checker (состояние на диске, переживает перезапуски).
	if !cfg.NoUpdateCheck {
		go func() {
			select {
			case <-time.After(15 * time.Second):
			case <-quit:
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			if err := checkPlugins(ctx, false); err != nil && !errors.Is(err, notify.ErrTooSoon) {
				slog.Warn("проверка обновлений плагинов не удалась", "err", err)
			}
		}()
	}
	// Токен Twitch может «протухнуть» (смена пароля, отзыв доступа), а новые функции — потребовать больше прав:
	// сообщаем об этом в центре уведомлений, даже если редактор закрыт.
	go func() {
		t := time.NewTicker(5 * time.Minute)
		defer t.Stop()
		select {
		case <-time.After(20 * time.Second):
		case <-quit:
			return
		}
		for {
			srv.SyncTwitchNotifications()
			select {
			case <-t.C:
			case <-quit:
				return
			}
		}
	}()
	if upd != nil {
		go func() {
			select {
			case <-time.After(10 * time.Second):
			case <-quit:
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			info, err := upd.Check(ctx)
			if err != nil {
				slog.Warn("проверка обновлений не удалась", "err", err)
			}
			keep := map[string]bool{}
			if info.Available {
				key := "app:" + info.Latest
				keep[key] = true
				center.Upsert(notify.Item{Key: key, Kind: notify.KindAppUpdate, Title: "New version " + info.Latest,
					Text: "Overlay Composer " + version + " → " + info.Latest, Data: mustJSON(map[string]string{
						"current": version, "latest": info.Latest, "url": info.URL, "notes": info.Notes})})
			}
			if err == nil { // при ошибке сети ничего не снимаем
				center.Resolve(notify.KindAppUpdate, keep) // версия уже установлена — напоминание снимается
			}
			if info.Available {
				slog.Info("доступна новая версия", "current", version, "latest", info.Latest, "url", info.URL)
				if tr != nil {
					tr.SetUpdate("Доступна версия " + info.Latest)
				}
			}
		}()
	}

	if *noTray {
		if first {
			openEditor()
		}
		<-quit
	} else {
		tr = tray.New(tray.Handlers{
			Title:        paths.AppTitle,
			Tooltip:      paths.AppTitle + " — " + srv.EditorURL(),
			Autostart:    platform.Autostart(),
			OnOpenEditor: openEditor,
			OnOpenData:   func() { _ = platform.OpenTarget(p.DataDir) },
			OnToggleAutostart: func() {
				if cur := platform.Autostart(); cur != nil {
					if err := platform.SetAutostart(!*cur); err != nil {
						slog.Error("автозапуск", "err", err)
					}
				}
				syncTray(tr)
			},
			OnUpdate: func() {
				if upd == nil {
					return
				}
				if i := upd.Info(); i.URL != "" {
					_ = platform.OpenTarget(i.URL)
				}
			},
			OnQuit: shutdown,
		})
		tr.Run(func() {
			if first {
				openEditor()
			}
		}) // блокирует до tr.Stop()
	}

	// Плавная остановка: плагины (oc.lifecycle.onStop) успевают сохранить данные, пока сервер ещё принимает их запросы.
	if r := h.NotifyStopping(stopGrace); r.Waited {
		slog.Info("плагины уведомлены об остановке", "pages", r.Notified, "noAnswer", r.TimedOut)
	}
	obsClient.Stop()
	srv.Shutdown()
	slog.Info("остановлено")
	return 0
}

// stopGrace — сколько времени плагины получают на сохранение данных при выходе из приложения.
const stopGrace = 3 * time.Second

func syncTray(t *tray.Tray) {
	if t != nil {
		t.SetAutostart(platform.Autostart())
	}
}

func stderrIsTerminal() bool {
	fi, err := os.Stderr.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func browseHost(host string) string {
	if host == "0.0.0.0" || host == "::" || host == "" {
		return "127.0.0.1"
	}
	return host
}

func editorURL(cfg config.Config) string {
	return "http://" + net.JoinHostPort(browseHost(cfg.Host), strconv.Itoa(cfg.Port)) + "/editor"
}

// ours — на адресе уже отвечает наше приложение (GET /api/app возвращает configFile).
func ours(host string, port int) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 1200*time.Millisecond)
	defer cancel()
	u := "http://" + net.JoinHostPort(browseHost(host), strconv.Itoa(port)) + "/api/app"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var v struct {
		ConfigFile string `json:"configFile"`
	}
	return resp.StatusCode == 200 && json.NewDecoder(resp.Body).Decode(&v) == nil && v.ConfigFile != ""
}

// openStore открывает базу; в пустой создаётся layout по умолчанию.
func openStore(p paths.Paths) (*store.Store, error) {
	st, err := store.Open(filepath.Join(p.DataDir, "overlay.db"))
	if err != nil {
		return nil, err
	}
	if err := st.Seed(); err != nil {
		st.Close()
		return nil, err
	}
	return st, nil
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
