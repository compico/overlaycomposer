# MEMO — для Claude: как устроен проект и что уже сделано

Файл для меня. Прочитать один раз — и можно работать. Что осталось сделать — в `TODO.md`. Общение с пользователем — по-русски.

## Что это
**Overlay Composer**: один OBS Browser Source `/overlay` рисует много iframe-виджетов через Three.js CSS3DRenderer.
Одна OBS-сцена → ровно один layout (нет привязки → default-layout → иначе остаётся прежний). Редактор — `/editor`.
Один бинарник на Go (Fiber v3), с треем, автозапуском, SQLite, встроенным клиентом (esbuild). Прокси для iframe-источников,
запрещающих встраивание (DonationAlerts и т.п.). Раньше был Deno — переписано на Go (после покупки Deno Cloudflare). Про Deno в README не упоминаем.

## Стек и версии
Go 1.26 (требует Fiber v3.5) · gofiber/fiber/v3 + contrib/v3/websocket · GORM + glebarez/sqlite (pure Go) · esbuild как Go-библиотека ·
fyne.io/systray · fasthttp/websocket (клиент obs-websocket и WS-мост прокси) · golang.org/x/net (html-токенайзер).
`CGO_ENABLED=0`, кросс-сборка без C. Windows GUI-сборка без консоли (`-H windowsgui`). go.mod/go.sum ведёт пользователь (`go mod tidy`).
Клиент: TypeScript + three.js, сборка `cmd/build` (esbuild), npm только ради three.

## Карта кода
```
cmd/overlay-composer/main.go   запуск: флаги (--home --autostart --no-open --no-tray --version), paths, config, store(+Seed), hub, obs,
                               server, трей, одиночный экземпляр (GET /api/app), openStore
cmd/build/main.go              сборка клиента esbuild → web/dist
internal/paths                 каталоги: Windows %APPDATA%\OverlayComposer, Linux ~/.config + ~/.local/share, портативный --home
internal/config                config.json (host/port/OBS) + валидация патчей из UI
internal/logx                  slog в файл/консоль
internal/model                 Source, Layout, Object, Transform, Canvas, ObsInfo; Validate, IsValidID
internal/store                 GORM+SQLite: sources, layouts (objects — JSON-колонка), layout_scenes (PK scene), default layout; Seed (демо)
internal/hub                   состояние: active layout, scene, списки сцен, пиры; Reresolve; ApplyObsData; HandleMessage; интерфейсы Conn/Storage/Obs
internal/obs                   клиент obs-websocket 5.x (fasthttp/websocket): сцены, смена, переименование, GetSceneItemList…
internal/obsimport             импорт OBS-сцены как layout (матрицы, alignment, поворот по часовой, группы разворачиваются в плоский список)
internal/proxy                 прокси (см. ниже)
internal/rewrite               NeutralizeFrameChecks — скрыть проверки «я во фрейме» (RE2 без lookaround)
internal/server                Fiber: server.go (routes, accessLog), api.go (REST), import.go, widgets.go (demo clock/box)
internal/tray                  значок в трее, меню
internal/platform              автозапуск (реестр Windows / .desktop), окно ошибки, открытие браузера
web/client/editor              редактор (main.ts, index.html): слои, drag/resize/rotate, Alt+клик — перебор объектов под курсором, импорт из OBS
web/client/runtime             /overlay: main.ts (WS, ?debug=1 HUD, clientLog)
web/client/shared/scene.ts     CSS3D-сцена, пул iframe по ключу sourceId#n (переиспользуется при смене layout), frameUrl() → /proxy/<id>/…
web/shared/types.ts            типы клиента ↔ сервера
```

## Модель данных и правила
- Source: `{id,name,type:"iframe",data:{url,proxy}}`. Object: `{id,name,sourceId,transform{x,y,z,width,height,rotationX/Y/Z,scaleX/Y/Z},opacity,visible,locked,zIndex}`.
- `Object.name` — имя слоя; пусто → имя source. Переименование source слои не меняет.
- Сцена принадлежит одному layout (PK `layout_scenes.scene`); сохранение layout забирает сцены у других в одной транзакции.
- `Reresolve` после изменения привязок переключает показ, только если для текущей сцены «положен» другой layout (ручной выбор не сбрасывается).
- Список сцен: obs-websocket приоритетнее, иначе от overlay (`obs:report` через window.obsstudio).
- Изменяющие `/api/app/*` требуют заголовок `x-overlay-composer: 1`.
- Данных старого JSON-формата нет: миграцию из JSON удалили (пользователь не выкладывал ту версию).

## Поток данных
```
OBS ──obs-websocket──► internal/obs ──► hub ──► /ws ──► overlay/editor
OBS Browser Source (window.obsstudio) ──/ws obs:report──► hub     (запасной канал)
editor ──REST──► server ──► store + hub.Broadcast
```
hub — один владелец состояния, одна блокировка; пир — канал + горутина-писатель; колбэки obs вне блокировок.

## Прокси (internal/proxy) — самая тонкая часть
- Маршруты: `/proxy/<id>/<путь>` (origin источника), `/proxy-h/<id>/<scheme>/<host>/…` (соседние хосты того же «сайта», SiteOf),
  `/proxy-ws/<id>/<ws|wss>/<host>/…` (WebSocket). Проксируются **только** origin-ы источников с `proxy:true` (не open proxy).
- `core.go` `Do()` — ядро без Fiber (тесты на httptest): выкидывает X-Frame-Options/CSP/COOP/COEP/CORP/Referrer-Policy/Set-Cookie…,
  Cookie переписывает на `Path=/proxy/<id>/`, редиректы того же origin → на mount, HTML/JS/CSS читает целиком (до 8 МБ), остальное потоком.
- HTML (`html.go`, токенайзер, без DOM): вставляет в начало `<head>` (после charset) `<meta referrer=same-origin>` + клиентский патч; убирает meta CSP;
  скрывает проверки «во фрейме»; **root-relative `href/src/srcset/action/poster/…` → `/proxy/<id>/…`** (`remountTag`).
- CSS (`css.go`): root-relative `url()`/`@import` → mount. JS: только NeutralizeFrameChecks.
- Клиентский патч (`patch.go`): fetch/XHR/EventSource/WebSocket → прокси; сеттеры `src/href` у script/link/img/iframe/… и `setAttribute` дописывают
  `/proxy/<id>` для root-relative; `frameElement` скрыт; **блок трекеров** `BlockedHosts` (mytopf.com, top-fwz1.mail.ru) → `data:` URL.
- Запасной путь без префикса (`fiber.go` `refererFallback` + `served.go`): запрос с `Referer: …/proxy/<id>/…` либо с Referer = путь, ранее отданный
  fallback-ом (Chromium/CEF шлёт URL модуля/стиля как Referer зависимостей) уходит на origin того же источника. Нужен для динамического `import('/…')`.
- `ws.go`: сначала dial upstream (Origin подменяется на origin источника, пересылаются Subprotocols/User-Agent), затем `websocket.New`.
  **Cookie/Authorization на WS-апстрим пока не пересылаются** (у trula из-за этого 401).
- Логи: ошибки ≥400 апстрима всегда (`proxy: ответ с ошибкой`); `http: 404` — запрос к нашему серверу без привязки (с referer); остальное при debug.

## Грабли, которые уже стоили времени (не повторять)
1. **Fiber `c.Append` склеивает значение с уже стоящим Content-Type (text/plain) через запятую** → CEF/Chromium отвергает стили и модули
   («MIME type text/plain»). Заголовки ответа выставлять через `c.Response().Header.Set/Add`. Это и было причиной «404/MIME» на DonationAlerts/StreamElements, а не Referer.
2. **Fiber переиспользует буфер запроса**: строки из `c.Get/OriginalURL/Params` нельзя хранить после запроса (кэши, горутины, WS-обработчики) — `strings.Clone`.
3. Fiber v3 требует Go ≥ 1.26; GORM/Fiber-API проверять по исходникам на GitHub, а не по памяти (`Iter`/`All()`, `errors.AsType`, `new(expr)` — стиль Go 1.26, пользователь любит современные идиомы, линтер строгий).
4. Выбранный объект при наложении перехватывал не тот элемент: у выбранной рамки `zIndex=1000000`, перебор — Alt+клик.
5. Сцена в OBS: CEF = Chromium 127, поведение Referer/MIME строже, чем в Firefox. Диагностика OBS: `?debug=1` на `/overlay`, `/api/client-log`, DevTools через
   `--remote-debugging-port=9222 --remote-allow-origins=http://localhost:9222`.
6. `html` внутри iframe высотой 8px — норма, если виджет без `height:100%` (стили у DonationAlerts не применялись из-за п.1).

## Как проверять в этой среде (облачный контейнер, без интернета кроме allowlist)
- proxy.golang.org недоступен; **`GOPROXY=direct GOSUMDB=off` работает** (через git на github.com). Не ходят vanity-хосты: `fyne.io`, `golang.org`, `gorm.io`,
  `modernc.org` → подставлять `replace` на локальные клоны github (`golang/net|sys|crypto|text|sync`, `go-gorm/gorm`, `fyne-io/systray`); sqlite-драйвер (modernc) не собирается,
  поэтому целиком `cmd/overlay-composer` здесь не собрать, а `internal/proxy` (с настоящим Fiber), `rewrite`, `hub`, `obsimport`, `model` — можно.
- Локальный Go: `scratchpad/go126/src126` (собран из исходников с `VERSION=go1.26.0`). Использовать: `GOROOT=…/src126 GOTOOLCHAIN=local PATH=…/src126/bin:$PATH`.
  Пересборка идёт минут 6 — запускать `setsid nohup … & disown` и опрашивать; не вызывать `pkill -f make.bash` из той же команды.
- Рабочие копии: `scratchpad/full` (проект + `replace`-ы + go.mod с `go 1.26.0`), `scratchpad/pxtest` (stdlib-прокси со stub x/net). Файлы копировать из `/home/claude/oc-go`.
- `go test -race ./internal/proxy/` проходит (включая `fiber_test.go`: Content-Type, cookie, потоки, Referer-цепочка).
- Не проверено: UI/Playwright редактора и overlay после Go-переписывания, настоящая Windows (трей, автозапуск), настоящий OBS, WS-мост прокси.

## Доставка
Архив: `zip -qr scratchpad/overlay-composer-go.zip . -x 'web/node_modules/*' 'build/*' '.git/*'` из `/home/claude/oc-go`, отправить `SendUserFile`.
Перед отправкой: `gofmt -l internal cmd`, тесты, обновить README/TODO/MEMO. В ответе — коротко и по-русски, что изменилось.

## Решения, принятые с пользователем
- SQLite + GORM, а не clover (документная БД не упрощает «сцена → один layout»; `store` за интерфейсом — замена возможна).
- Перезагрузка виджета при смене сцены допустима (скрытые не держим в памяти) — но копии одного виджета в разных layout надо не перезагружать (см. TODO «Общий экземпляр»).
- Один бинарник, трей, автозапуск, AppData; прокси встроен.
