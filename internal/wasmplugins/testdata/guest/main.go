// Тестовый плагин для интеграционного теста wazero (wazero_test.go), написанный на SDK pkg/ocplugin. Сборка:
//
//	GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o testguest.wasm .
package main

import (
	"os"
	"time"

	"github.com/compico/overlaycomposer/pkg/ocplugin"
)

func init() {
	p := ocplugin.New(ocplugin.Meta{
		ID: "testguest", Name: "Test guest", Version: "0.1.0",
		Permissions: []string{ocplugin.PermStorage, ocplugin.PermTimer},
	})
	p.OnInit(func() error { return ocplugin.SetTimer("t", 50*time.Millisecond, true) })
	p.OnTimer("t", func() {
		n, _, _ := ocplugin.Load[int]("ticks")
		_ = ocplugin.Store("ticks", n+1)
	})
	p.GET("/greet", func(c *ocplugin.Ctx) error {
		n, _, _ := ocplugin.Load[int]("greeted")
		n++
		if err := ocplugin.Store("greeted", n); err != nil {
			ocplugin.Warn("store failed", "err", err.Error())
		}
		return c.JSON(200, map[string]any{"hello": c.Query("name"), "count": n})
	})
	p.GET("/ticks", func(c *ocplugin.Ctx) error {
		n, _, _ := ocplugin.Load[int]("ticks")
		return c.JSON(200, map[string]int{"ticks": n})
	})
	// Паника в обработчике: SDK превращает её в 500 и пишет в лог, экземпляр остаётся жив.
	p.GET("/boom", func(*ocplugin.Ctx) error {
		var m map[string]int
		m["x"] = 1
		return nil
	})
	// Настоящий сбой рантайма (процесс wasm завершился): хост должен пересоздать экземпляр.
	p.GET("/exit", func(*ocplugin.Ctx) error { os.Exit(3); return nil })
}

func main() {}
