// Package tray — значок в системном трее (fyne.io/systray: Windows — Win32, Linux — StatusNotifierItem по D-Bus, без cgo).
package tray

import (
	"log/slog"
	"runtime"
	"sync"
	"sync/atomic"

	"fyne.io/systray"

	"github.com/compico/overlaycomposer/assets"
)

type Handlers struct {
	Title     string
	Tooltip   string
	Autostart *bool // nil — пункт не показываем

	OnOpenEditor      func()
	OnOpenData        func()
	OnToggleAutostart func()
	OnUpdate          func() // клик по пункту «Доступна версия …»
	OnQuit            func()
}

type Tray struct {
	h       Handlers
	running atomic.Bool
	auto    *systray.MenuItem
	upd     *systray.MenuItem
	mu      sync.Mutex
	pending string // заголовок пункта обновления, пришедший до создания меню
}

func New(h Handlers) *Tray { return &Tray{h: h} }

// Running — значок создан и цикл сообщений работает.
func (t *Tray) Running() bool { return t.running.Load() }

// Run блокирует горутину до выхода и ДОЛЖНА вызываться из main goroutine (systray требует главный поток ОС).
// ready вызывается, когда значок создан (оттуда можно открывать редактор и т.п.).
func (t *Tray) Run(ready func()) {
	runtime.LockOSThread()
	systray.Run(func() {
		t.running.Store(true)
		icon := assets.PNG
		if runtime.GOOS == "windows" {
			icon = assets.ICO
		}
		systray.SetIcon(icon)
		systray.SetTitle(t.h.Title)
		systray.SetTooltip(t.h.Tooltip)

		open := systray.AddMenuItem("Открыть редактор", "")
		data := systray.AddMenuItem("Открыть папку с данными", "")
		if t.h.Autostart != nil {
			systray.AddSeparator()
			t.auto = systray.AddMenuItemCheckbox("Запускать при входе в систему", "", *t.h.Autostart)
		}
		t.mu.Lock()
		t.upd = systray.AddMenuItem("", "")
		t.upd.Hide()
		if t.pending != "" {
			t.upd.SetTitle(t.pending)
			t.upd.Show()
		}
		updCh := t.upd.ClickedCh
		t.mu.Unlock()
		systray.AddSeparator()
		quit := systray.AddMenuItem("Выход", "")

		// Обработчики — в отдельных горутинах цикла, чтобы долгий клик не вешал меню.
		go func() {
			var autoCh <-chan struct{}
			if t.auto != nil {
				autoCh = t.auto.ClickedCh
			}
			for {
				select {
				case <-open.ClickedCh:
					call(t.h.OnOpenEditor)
				case <-data.ClickedCh:
					call(t.h.OnOpenData)
				case <-autoCh:
					call(t.h.OnToggleAutostart)
				case <-updCh:
					call(t.h.OnUpdate)
				case <-quit.ClickedCh:
					call(t.h.OnQuit)
					return
				}
			}
		}()
		if ready != nil {
			ready()
		}
	}, func() { t.running.Store(false) })
}

// SetUpdate показывает в меню пункт «Доступна версия …» (пустая строка — скрывает).
func (t *Tray) SetUpdate(title string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pending = title
	if t.upd == nil {
		return
	}
	if title == "" {
		t.upd.Hide()
		return
	}
	t.upd.SetTitle(title)
	t.upd.Show()
}

// SetAutostart обновляет галочку (после смены из UI редактора).
func (t *Tray) SetAutostart(v *bool) {
	if t.auto == nil || v == nil {
		return
	}
	if *v {
		t.auto.Check()
	} else {
		t.auto.Uncheck()
	}
}

// Stop завершает systray.Run.
func (t *Tray) Stop() { systray.Quit() }

func call(f func()) {
	if f == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			slog.Error("tray handler panic", "err", r)
		}
	}()
	f()
}
