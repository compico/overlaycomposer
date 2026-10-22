// Overlay Composer — один бинарник: HTTP/WS-сервер, редактор, runtime для OBS, значок в трее.
package main

import (
	"context"
	"encoding/json"
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
	"github.com/compico/overlaycomposer/internal/obs"
	"github.com/compico/overlaycomposer/internal/paths"
	"github.com/compico/overlaycomposer/internal/platform"
	"github.com/compico/overlaycomposer/internal/plugins"
	"github.com/compico/overlaycomposer/internal/proxy"
	"github.com/compico/overlaycomposer/internal/server"
	"github.com/compico/overlaycomposer/internal/store"
	"github.com/compico/overlaycomposer/internal/tray"
	"github.com/compico/overlaycomposer/internal/twitch"
)

// version подставляется при сборке: -ldflags "-X main.version=1.2.3"
var version = "dev"

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

	authMgr, err := auth.New(auth.FromStore(st))
	if err != nil {
		slog.Error("авторизация устройств", "err", err)
		platform.ErrorBox(paths.AppTitle, "Не удалось подготовить авторизацию устройств: "+err.Error())
		return 1
	}

	twMgr := twitch.New(twitch.FromStore(st))
	chatSvc := chat.New(twMgr)
	defer chatSvc.Close()

	pluginReg := plugins.New(plugins.Builtin(), os.DirFS(p.PluginsDir))
	mk, err := market.New(market.Config{
		File: filepath.Join(p.DataDir, "market.json"), Dir: p.PluginsDir, Reg: pluginReg, Builtin: pluginReg.IsBuiltin,
	})
	if err != nil {
		slog.Error("магазин плагинов", "err", err)
		platform.ErrorBox(paths.AppTitle, "Не удалось прочитать список магазина плагинов: "+err.Error())
		return 1
	}

	srv = server.New(server.Deps{
		Paths: p, Cfg: cfgStore, Running: cfg, Store: st, Hub: h, Obs: obsClient, Proxy: px,
		Auth:    authMgr,
		Twitch:  twMgr,
		Chat:    chatSvc,
		KV:      kv.New(st.KV()),
		Plugins: pluginReg,
		Market:  mk,
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
			OnQuit: shutdown,
		})
		tr.Run(func() {
			if first {
				openEditor()
			}
		}) // блокирует до tr.Stop()
	}

	obsClient.Stop()
	srv.Shutdown()
	slog.Info("остановлено")
	return 0
}

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
