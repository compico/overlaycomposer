# PoC: серверные wasm-плагины

Исследование (не часть приложения): можно ли расширять OC серверными плагинами в WebAssembly — с жизненным циклом, HTTP-маршрутами,
вызовами хоста и перезапуском упавшего плагина. Обсуждение и решения — в issue.

## Состав (v2)

- `guest/` — плагин «hello» на Go (`GOOS=wasip1`, reactor, `//go:wasmexport`). Метаданные с маршрутами, обработчик `greet`
  (состояние через storage хоста, лог), обработчик `boom` (паника).
- `node/harness.mjs` — «хост» на Node: проверяет сам ABI без Go-зависимостей.
- `music/` — плагин «заявки на треки» (реальная логика из #103: разбор ссылки, очередь с дедупликацией, подтверждение старта,
  баллы возвращаются при отказе). Логика отдельно от ABI (`logic.go`), проверяется обычными Go-тестами (`go test`);
  `abi_wasip1.go` — обёртка.
- `rust/` — Rust-версия `hello` (serde, `cdylib`, `wasm32-wasip1`) — тот же ABI.
- `host/` — тот же хост на Go + wazero (v1.12.0) с HTTP-маршрутизацией и перезапуском. Запущен вручную на Windows:
  `greet` считает через storage хоста, `boom` → 500, паника и её трасса попадают в лог с `plugin=hello`, экземпляр
  перезапускается, счётчик не теряется.

## ABI (сигнатуры не меняются, всё остальное — JSON поверх них)

| Кто | Функция | Назначение |
|---|---|---|
| плагин экспортирует | `alloc(n) -> ptr`, `dealloc(ptr)` | память под данные хоста |
| плагин экспортирует | `oc_init(ptr, len) -> i64` | `{apiVersion, config}` → метаданные (id, версия, маршруты) |
| плагин экспортирует | `oc_call(name_ptr, name_len, arg_ptr, arg_len) -> i64` | вызов обработчика/события по имени |
| хост импортирует (`oc`) | `host_call(op_ptr, op_len, arg_ptr, arg_len) -> i64` | `log`, `storage.get/set`, дальше `http.request`, `ws.*`, `timer.*` |

`i64 = ptr << 32 | len` — буфер в памяти плагина, `0` — пусто. Хост читает возвращённый буфер и освобождает его через `dealloc`.

## События, таймеры, асинхронный HTTP

Плагин пассивен, поэтому долгоживущее держит хост, а плагин реагирует. Всё через `oc_call("on_event", {"type", "data"})` и `host_call`:

| Что | Как |
|---|---|
| Событие Twitch | хост → `on_event {type:"twitch.redemption", data:{id, rewardId, userLogin, input}}` |
| Таймер | плагин: `host_call("timer.set", {id, ms, repeat})` (хост владеет таймером, он переживает перезапуск экземпляра) → `on_event {type:"timer", data:{id}}` |
| Исходящий HTTP | плагин: `host_call("http.fetch", {id, method, url, body})` возвращается сразу; ответ → `on_event {type:"http.response", data:{id, status, body, error}}` |
| Погашение Twitch | плагин: `host_call("twitch.redemption.update", {rewardId, id, status})` (в PoC — заглушка) |
| Аддон плеера | обычные маршруты плагина: `GET /next` (идемпотентный опрос, `resend` для отданной без подтверждения), `POST /started`, `POST /failed` |

Обработчик маршрута **отвечает сразу** и ничего не ждёт: долгая работа (HTTP) идёт асинхронно, результат приходит событием и кладётся в
storage. Поэтому один экземпляр с мьютексом не блокируется на сети.

## Запуск проверок

```bash
# логика music (обычные тесты Go)
cd music && go test ./...
# ABI и события на Node
cd music && GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o music.wasm .
cd ../guest && GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o hello.wasm .
cd ../node && node harness.mjs && node harness-music.mjs
# Go-хост: параллельность, trap/перезапуск, таймер (нужна сеть для go mod tidy)
cd ../host && go mod tidy && go test -race -count=1 ./...
# Rust
cd ../rust && rustup target add wasm32-wasip1 && cargo build --release --target wasm32-wasip1
cd ../host && go run . ../rust/target/wasm32-wasip1/release/hello_rs.wasm
```

## Запуск проверки ABI (первая версия)

```bash
cd guest && GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o hello.wasm .
cd ../node && node harness.mjs     # Node 22+
```

## Что показал PoC (Node)

- ABI работает: метаданные, обработчики, лог плагина в хост, состояние в storage хоста.
- Вложенный вызов `alloc` плагина из `host_call` (хост отвечает данными, выделяя память в плагине) работает.
- Паника плагина = trap (`RuntimeError`), хост жив; текст паники плагин пишет в stderr — хост может перехватить его в лог.
- После перезапуска экземпляра состояние (в хосте) сохраняется.
- Размер `hello.wasm` на Go с `encoding/json` ≈ 3 МБ: для лёгких плагинов это много (Rust/TinyGo дали бы на порядок меньше — не проверено).
- v2: `music` целиком работает через события и маршруты (`node harness-music.mjs`, 15 проверок, включая sad path); перезапуск экземпляра
  не теряет очередь (она в storage хоста); опрос `GET /next` идемпотентен.
- Rust: `cargo check` проходит (нативно), сборка в wasm и запуск не проверялись (цель wasm32 в среде автора не ставится) — проверьте
  у себя, обратите внимание на размер `.wasm` и поведение паники (текст уходит в лог хоста через panic hook).
- Go-хост v2 проверен владельцем репозитория на Windows (wazero v1.12.0): `go test -race` (параллельные вызовы, trap с перезапуском, таймер) зелёный.
- Rust (`hello-rs`): wasm32-wasip1 собирается и работает: `.wasm` ≈ 115 КБ (Go-версия ≈ 3 МБ), счётчик в storage хоста переживает паники (`/boom` → 500, плагин перезапущен).
- Сценарий заявки целиком автоматизирован в `host_test.go` (`TestMusicFlowOverHTTP`): событие → `/next` → `/started` → `FULFILLED`, плюс sad path.
