# Как добавить разрешение или событие wasm-плагинам

Для разработчиков OC. Новое разрешение (`chat.read`), подписка (`chat.message`) или операция хоста живут в нескольких местах
**трёх репозиториев**. Забытое место ломает сборку или работу плагина, причём не там, где его забыли. Пример: список разрешений
в `scripts/wasm.mjs` репозитория oc-plugins не знал `chat.read`, и сборка плагина падала с «неизвестное разрешение».

Порядок: PR в `compico/overlaycomposer` → merge → плагин в `compico/oc-plugins` → документация на `compico.github.io`.
Чек-лист ниже в том же порядке; формат самого разрешения описан в [`WASM_PLUGINS.md`](WASM_PLUGINS.md).

## 1. overlaycomposer (один PR)

**Версия API (всегда, если добавляется право, событие или поле меты)**

- [ ] `internal/wasmplugins/meta.go`: поднять `APIVersion` на 1 и дописать в комментарий, что вошло в новую версию. Для нового права или события добавить запись в `permMinAPI` / `eventMinAPI` (с какой версии API они доступны): `Validate` потребует от плагина `apiVersion` не ниже.
- [ ] SDK (`pkg/ocplugin`): `apiVersionNeeded` возвращает новую версию для плагина, который использует новую возможность, иначе плагин объявит прежний API и старое приложение отклонит его по «unknown permission», а не по версии. Тест `TestAPIVersionNeeded`.
- [ ] Версия API показывается в настройках редактора (`pluginApi`) сама — ничего делать не нужно.

**Разрешение**

- [ ] `internal/wasmplugins/meta.go`: константа `Perm…` с комментарием «что это даёт» и запись в `knownPerms`.
- [ ] Редактор: `PERMISSION_TEXT` в `web/client/editor/features/wasmplugins/model.ts`, человеческое описание права (то, что видит пользователь в окне прав). Тест `model.test.ts` читает `meta.go` и падает, если у известного права нет описания.

**Событие (подписка)**

- [ ] `meta.go`: константа `Event…` и запись в `knownEvents`; в `Validate` правило «подписка требует разрешения» (и прочие условия вроде непустого списка).
- [ ] `internal/wasmplugins/plugin.go`: если событие идёт особым путём (как `chat.command`, `chat.message`), общий `Deliver` должен его **отклонять**, а отдельный `Deliver…` проверять и подписку, и выдачу права.
- [ ] `internal/wasmplugins/manager.go`: `Publish…` для рассылки и учёт нового права в `Wants` (иначе источник события не включится).
- [ ] Источник события на сервере (например, `internal/server/wasm.go`): подписка включается, только пока есть плагин с подпиской и выданным правом, и выключается без них; код nil-безопасен. Пересчёт вызывается там, где меняются выданные права и список плагинов.

**Операция хоста (`host_call`)**

- [ ] `plugin.go`: запись «операция → право» в карте проверки (рядом с `chat.send`) и сам обработчик.

**SDK (`pkg/ocplugin`)**

- [ ] Константа `Perm…`, обработчик `p.On…`, добавление подписки в `metaJSON`, разбор события в `dispatch`, типы данных.
- [ ] Тест SDK. `New` допускается один раз на плагин: перед вторым `New` в тесте нужен `current = nil`.

**Документация и тесты**

- [ ] `docs/WASM_PLUGINS.md`: подписка, таблица событий, формат данных, строка в списке прав.
- [ ] Тесты в `internal/wasmplugins`: `Validate` (подписка без права отклоняется), доставка (нет права, нет подписки, общий `Deliver` не пропускает), в `pkg/ocplugin`: разбор события.

## 2. oc-plugins (после merge в `dev`)

- [ ] `scripts/wasm.mjs`: `PERMS`, `EVENTS`, `MAX_API` (максимальная `apiVersion`), `PERM_API` / `EVENT_API` (с какой версии API доступны право и событие) и правило «подписка требует права» в `checkMeta`; тест в `test/wasm-scripts.test.mjs`. Это **единственная** проверка метаданных wasm-плагинов в CI: `ocpack` права не проверяет.
- [ ] `scripts/validate.mjs` относится только к HTML-плагинам (`plugins/`), там свой список `PERMS`; для wasm-плагинов его не трогаем.
- [ ] Плагин подключает SDK из `dev`: в `go.mod` версия вида `v0.0.0-<UTC-время коммита>-<12 символов>`. Если нужной версии ещё нет в прокси, `go mod tidy` с доступом к сети допишет `go.sum`.
- [ ] Новая версия плагина, `changelog.json` (в `migration` сказать, какое разрешение выдать), README плагина.

## 3. compico.github.io

- [ ] Гайды `docs/plugin-development` и `docs/plugins` (EN и RU): строка в таблице разрешений, раздел про событие, если оно видно автору плагина.
- [ ] Дайджест релиза, если пользователь увидит новое разрешение в окне прав.

## Проверка

```
cd pkg/ocplugin && GOTOOLCHAIN=local go test ./...    # SDK: хватает Go 1.24
cd web && node --test --experimental-strip-types client/editor/features/wasmplugins/model.test.ts
cd oc-plugins && npm test
```

Основное приложение (`internal/…`) требует Go 1.26, локально без него тесты `internal/wasmplugins` и `internal/server` проходят только в CI;
перед пушем обязательно `gofmt -l internal pkg`.

## Если что-то забыли

| Симптом | Где не хватает |
|---|---|
| Сборка плагина: «неизвестное разрешение / подписка» | `scripts/wasm.mjs` в oc-plugins (`PERMS`, `EVENTS`) |
| Плагин не запускается: `subscription … requires permission` | правило в `Validate` и `knownPerms` / `knownEvents` в `meta.go` |
| В окне прав сырое имя права вместо текста | `model.ts` (описание права) |
| Право выдано, события не приходят | `Deliver…` / `Wants` в `plugin.go`, `manager.go` или источник в `internal/server/wasm.go` |
| `ocplugin: New is called twice` в тесте SDK | нет `current = nil` перед вторым `New` |
| Приложение пишет «unknown permission» вместо «нужен API N» | плагин объявил `apiVersion` ниже нужного: не обновлён `apiVersionNeeded` в SDK или `permMinAPI` / `eventMinAPI` |
| Хеш `go.sum` не сходится | `go mod tidy` с сетью; версия в `go.mod` должна быть коммитом, который есть в `dev` |
