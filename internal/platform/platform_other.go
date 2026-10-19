//go:build !windows

package platform

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

func openTarget(target string) error {
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	cmd := exec.Command(name, target)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// errorBox: на Linux пробуем zenity / kdialog; если их нет — сообщение уже выведено в stderr.
func errorBox(title, msg string) {
	switch runtime.GOOS {
	case "darwin":
		script := fmt.Sprintf(`display dialog %q with title %q buttons {"OK"} with icon stop`, msg, title)
		_ = exec.Command("osascript", "-e", script).Run()
	default:
		if p, err := exec.LookPath("zenity"); err == nil {
			_ = exec.Command(p, "--error", "--title", title, "--text", msg).Run()
		} else if p, err := exec.LookPath("kdialog"); err == nil {
			_ = exec.Command(p, "--error", msg, "--title", title).Run()
		}
	}
}

// ---- автозапуск: ~/.config/autostart/overlay-composer.desktop (только Linux) ----

func desktopFile() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "autostart", "overlay-composer.desktop")
}

func getAutostart() *bool {
	if runtime.GOOS != "linux" {
		return nil
	}
	_, err := os.Stat(desktopFile())
	on := err == nil
	return &on
}

func desktopEntry(exe string) string {
	q := `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(exe) + `"`
	return "[Desktop Entry]\nType=Application\nName=Overlay Composer\nExec=" + q + " " + AutostartFlag + "\nTerminal=false\nX-GNOME-Autostart-enabled=true\n"
}

func setAutostart(enabled bool, exe string) error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("autostart is not supported on %s", runtime.GOOS)
	}
	f := desktopFile()
	if !enabled {
		if err := os.Remove(f); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
		return err
	}
	return os.WriteFile(f, []byte(desktopEntry(exe)), 0o644)
}
