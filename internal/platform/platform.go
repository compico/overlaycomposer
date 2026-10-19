// Package platform — то, что отличается между ОС: открыть файл/URL, окно с ошибкой, автозапуск.
package platform

import (
	"fmt"
	"os"
	"runtime"
)

const AutostartFlag = "--autostart"

// OS — имя ОС в терминах API (windows | linux | darwin).
func OS() string { return runtime.GOOS }

// OpenTarget открывает URL, файл или каталог программой по умолчанию.
func OpenTarget(target string) error { return openTarget(target) }

// ErrorBox показывает ошибку пользователю. У GUI-сборки под Windows консоли нет, поэтому это окно — единственный способ
// сообщить, что приложение не запустилось.
func ErrorBox(title, msg string) {
	fmt.Fprintf(os.Stderr, "%s: %s\n", title, msg)
	errorBox(title, msg)
}

// Autostart: nil — не поддерживается на этой ОС, иначе включён ли автозапуск.
func Autostart() *bool { return getAutostart() }

// SetAutostart включает/выключает запуск при входе в систему (с флагом --autostart: без открытия браузера).
func SetAutostart(enabled bool) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return setAutostart(enabled, exe)
}
