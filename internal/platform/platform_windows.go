//go:build windows

package platform

import (
	"errors"
	"fmt"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const (
	runKey  = `Software\Microsoft\Windows\CurrentVersion\Run`
	runName = "OverlayComposer"
)

func openTarget(target string) error {
	verb, _ := windows.UTF16PtrFromString("open")
	file, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	return windows.ShellExecute(0, verb, file, nil, nil, windows.SW_SHOWNORMAL)
}

func errorBox(title, msg string) {
	t, _ := windows.UTF16PtrFromString(title)
	m, _ := windows.UTF16PtrFromString(msg)
	_, _ = windows.MessageBox(0, m, t, windows.MB_OK|windows.MB_ICONERROR)
}

func autostartCommand(exe string) string { return fmt.Sprintf(`"%s" %s`, exe, AutostartFlag) }

func getAutostart() *bool {
	on := false
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err == nil {
		defer k.Close()
		if v, _, err := k.GetStringValue(runName); err == nil && v != "" {
			on = true
		}
	}
	return &on
}

func setAutostart(enabled bool, exe string) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if enabled {
		return k.SetStringValue(runName, autostartCommand(exe))
	}
	if err := k.DeleteValue(runName); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return err
	}
	return nil
}
