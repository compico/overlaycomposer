# ocplugin — SDK серверных wasm-плагинов на Go

Отдельный модуль без зависимостей (только stdlib): `go get github.com/compico/overlaycomposer/pkg/ocplugin`
не тянет за собой код приложения и не требует его версии Go (нужен Go 1.24+ для `//go:wasmexport`).

```go
package main

import "github.com/compico/overlaycomposer/pkg/ocplugin"

func init() {
	p := ocplugin.New(ocplugin.Meta{
		ID: "hello", Name: "Hello", Version: "0.1.0",
		Permissions: []string{ocplugin.PermStorage},
	})
	p.GET("/greet", func(c *ocplugin.Ctx) error {
		n, _, _ := ocplugin.Load[int]("count")
		_ = ocplugin.Store("count", n+1)
		return c.JSON(200, map[string]any{"hello": c.Query("name"), "count": n + 1})
	})
}

func main() {} // обязателен для сборки reactor, остаётся пустым
```

```
GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o hello.wasm .
```

Положите `hello.wasm` в папку `ocplugs`. Маршрут: `/plugins/hello/api/greet?name=oc`.

## API

| | |
|---|---|
| `New(Meta)` | создать плагин (один раз, из `init()`) |
| `p.GET/POST/PUT/PATCH/DELETE(path, func(*Ctx) error)` | маршруты `/plugins/<id>/api<path>` |
| `Ctx`: `Query`, `Bind(&v)`, `JSON(status, v)`, `NoContent()`, `Error(status, msg)` | запрос и ответ |
| `p.OnInit`, `p.Config(&v)` | запуск экземпляра и настройки пользователя |
| `p.OnRedemption`, `p.OnTimer(id, f)`, `p.OnHTTPResponse(id, f)` | события хоста |
| `Load[T]`, `Store`, `Remove` | хранилище (`PermStorage`) |
| `SetTimer`, `ClearTimer` | таймеры хоста (`PermTimer`) |
| `Fetch(id, FetchRequest)` | HTTP-запрос хоста, ответ — в `OnHTTPResponse` (`PermNetHTTP`) |
| `FulfillRedemption`, `CancelRedemption` | статус погашения награды (`PermRedeem`) |
| `Debug/Info/Warn/Error(msg, kv...)` | лог приложения с пометкой плагина |

## Что важно знать

- **Плагин пассивен.** В `wasip1` нет потоков: горутины идут только внутри вызова хоста, фоновое ожидание не работает.
  Фоновая работа — таймеры хоста и `Fetch`. Обработчик маршрута отвечает сразу.
- **Состояние — в `Store`/`Load`, не в переменных:** экземпляр пересоздаётся после сбоя.
- **Паника в обработчике** не роняет плагин: SDK отвечает 500 (подробности только в лог плагина) и продолжает работу.
  Сбой процесса wasm хост лечит перезапуском экземпляра.
- **Метаданные собираются сами** (маршруты, подписка на награды); разрешения перечисляются в `Meta` явно — пользователь их выдаёт.
- **Размер:** Go-плагин ≈ 3 МБ (рантайм Go). Rust-плагин на том же ABI — около 100 КБ.
- **Тесты на обычном `go test`:** `ocplugin.SetHost(func(op string, arg []byte) []byte {…})` подменяет хост;
  обработчики вызываются как обычные функции. Слой ABI собирается только под `wasip1`.

Подробности ABI — `docs/WASM_PLUGINS.md`.
