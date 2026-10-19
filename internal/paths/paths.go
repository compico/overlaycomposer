// Package paths решает, где лежат настройки, данные и логи.
//
//   - --home <dir> или OVERLAY_HOME: всё в одном каталоге (config.json, data/, logs/) — переносимый режим, разработка, тесты.
//   - иначе — профиль пользователя:
//     Windows: %APPDATA%\OverlayComposer\            (config.json, data\, logs\)
//     Linux:   ~/.config/overlay-composer/config.json
//     ~/.local/share/overlay-composer/{data,logs}   ($XDG_CONFIG_HOME / $XDG_DATA_HOME учитываются)
//     macOS:   ~/Library/Application Support/OverlayComposer/
package paths

import (
	"os"
	"path/filepath"
	"runtime"
)

const (
	AppID    = "overlay-composer"
	AppTitle = "Overlay Composer"
)

type Paths struct {
	Mode       string // "home" | "user"
	ConfigFile string
	DataDir    string // база данных, импорт и т.п.
	LogDir     string
	LogFile    string
}

// Resolve вычисляет пути и создаёт каталоги. home — значение флага --home (может быть пустым).
func Resolve(home string) (Paths, error) {
	if home == "" {
		home = os.Getenv("OVERLAY_HOME")
	}
	var p Paths
	if home != "" {
		root, err := filepath.Abs(home)
		if err != nil {
			return p, err
		}
		p = Paths{Mode: "home", ConfigFile: filepath.Join(root, "config.json"), DataDir: filepath.Join(root, "data"), LogDir: filepath.Join(root, "logs")}
	} else {
		p = userPaths()
	}
	p.LogFile = filepath.Join(p.LogDir, "overlay-composer.log")
	for _, d := range []string{filepath.Dir(p.ConfigFile), p.DataDir, p.LogDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return p, err
		}
	}
	return p, nil
}

func env(name string) string { return os.Getenv(name) }

func userPaths() Paths {
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "windows":
		base := env("APPDATA")
		if base == "" {
			base = filepath.Join(home, "AppData", "Roaming")
		}
		root := filepath.Join(base, "OverlayComposer")
		return Paths{Mode: "user", ConfigFile: filepath.Join(root, "config.json"), DataDir: filepath.Join(root, "data"), LogDir: filepath.Join(root, "logs")}
	case "darwin":
		root := filepath.Join(home, "Library", "Application Support", "OverlayComposer")
		return Paths{Mode: "user", ConfigFile: filepath.Join(root, "config.json"), DataDir: filepath.Join(root, "data"), LogDir: filepath.Join(root, "logs")}
	default:
		cfg := env("XDG_CONFIG_HOME")
		if cfg == "" {
			cfg = filepath.Join(home, ".config")
		}
		data := env("XDG_DATA_HOME")
		if data == "" {
			data = filepath.Join(home, ".local", "share")
		}
		root := filepath.Join(data, AppID)
		return Paths{Mode: "user", ConfigFile: filepath.Join(cfg, AppID, "config.json"), DataDir: filepath.Join(root, "data"), LogDir: filepath.Join(root, "logs")}
	}
}
