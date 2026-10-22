# Как сделать репозиторий с плагинами

Пошаговый гайд: от пустой папки до репозитория, из которого плагины ставятся кнопкой в **Add Source → Plugin store…**.
Справочник по манифесту и API — в [`PLUGINS.md`](PLUGINS.md); пример готового репозитория — [compico/oc-plugins](https://github.com/compico/oc-plugins).

## 1. Правила, которых требует магазин

Магазин скачивает репозиторий zip-архивом и **ничего не собирает** — ставится ровно то, что лежит в `plugins/` в выбранной ветке/теге.
Поэтому:

```
my-plugins/                 ← репозиторий GitHub (публичный)
  plugins/
    hello/                  ← имя папки = "id" из plugin.json
      plugin.json           ← обязателен
      index.html            ← entry (по умолчанию index.html)
      …                     ← всё остальное, что нужно виджету (готовые, уже собранные файлы)
    another/
      …
```

- Каталог создаётся из папок `plugins/<id>/`, где есть валидный `plugin.json` и существующий `entry`. Папки с ошибками в манифесте просто не попадают в каталог.
- `id` — латиница, цифры, `_`, `-` (до 64 символов), **совпадает с именем папки**. Встроенные id (`twitch-chat`) и уже занятые другим установленным плагином ставить нельзя.
- `version` — число через точки (`1.2.0`). Пользователю предлагается обновление, когда версия в репозитории выше установленной: **поднимайте её при каждом изменении**.
- Лимиты: файл до 16 МБ, плагин до 64 МБ, архив репозитория до 64 МБ (поэтому не кладите в репозиторий `node_modules`, видео и т. п.), не больше 5000 файлов. Симлинки игнорируются.
- Если плагин собирается (Vite, TypeScript и т. д.), **результат сборки коммитится** в `plugins/<id>/`. Исходники лежат в другом месте (например, `src/`).
- Пользователь может подключить ветку по умолчанию (`owner/repo`) или закрепить ветку/тег (`owner/repo@v1.0.0`).

## 2. Минимальный плагин без сборки

`plugins/hello/plugin.json`:

```json
{
  "id": "hello",
  "name": "Hello",
  "version": "1.0.0",
  "description": "Показывает последнее сообщение чата",
  "permissions": ["chat.read"],
  "defaultSize": { "width": 480, "height": 80 },
  "settings": [
    { "key": "color", "label": "Цвет текста", "type": "color", "default": "#ffffff" }
  ]
}
```

`plugins/hello/index.html`:

```html
<!doctype html>
<meta charset="utf-8">
<div id="app">…</div>
<script src="/plugin-sdk/oc.js"></script>
<script type="module">
  const app = document.getElementById("app");
  app.style.color = oc.param("color", "#ffffff");
  await oc.chat.subscribe();
  oc.chat.on("message", (m) => { app.textContent = `${m.displayName}: ${m.text}`; });
</script>
```

Положите папку `hello` в `plugins/` рядом с данными OC (Add Source → **Open plugins folder**) и добавьте виджет — так можно отлаживать без публикации.
Запушьте репозиторий в GitHub, и он готов к подключению в магазине.

## 3. TypeScript: как подключить заголовок `oc.d.ts`

Весь API плагинов описан одним файлом типов — [`web/sdk/oc.d.ts`](../web/sdk/oc.d.ts). Он объявляет глобальную переменную `oc` и типы
(`OCChatMessage`, `OCPermission` и др.). Скачайте его в проект (из запущенного OC или из репозитория):

```sh
mkdir -p types
curl -o types/oc.d.ts http://127.0.0.1:8000/plugin-sdk/oc.d.ts      # порт — из настроек OC
# или: curl -o types/oc.d.ts https://raw.githubusercontent.com/compico/overlaycomposer/main/web/sdk/oc.d.ts
```

В `tsconfig.json` включите папку с ним — глобальный `oc` появится во всех `.ts`-файлах:

```json
{
  "compilerOptions": {
    "target": "ES2022", "module": "ESNext", "moduleResolution": "bundler",
    "lib": ["ES2022", "DOM"], "strict": true, "noEmit": true, "skipLibCheck": true
  },
  "include": ["src", "types"]
}
```

Для плагина на чистом JS работает `// @ts-check` + `/// <reference path="../types/oc.d.ts" />` в начале файла, либо `jsconfig.json` с тем же `include`.
Обновляйте `oc.d.ts` при выходе новой версии OC (`oc.version` — версия API). Этот же файл можно целиком отдать ИИ-помощнику вместе с описанием задачи.

Сам **`oc.js` в бандл не включается** — он подключается обычным тегом `<script src="/plugin-sdk/oc.js">` (его отдаёт OC; это и есть мост к чату и хранилищу).

## 4. Сборка: Vite (рекомендуется для TypeScript/фреймворков)

Структура: исходники в `src/<id>/`, результат сборки — в `plugins/<id>/` (его коммитите).

```
my-plugins/
  package.json
  tsconfig.json
  types/oc.d.ts
  scripts/build.mjs
  src/hello/
    index.html          ← точка входа Vite
    main.ts
    style.css
    public/plugin.json  ← всё из public/ Vite копирует в результат как есть
  plugins/hello/        ← результат сборки (коммитится)
```

`package.json`:

```json
{
  "name": "my-oc-plugins", "private": true, "type": "module",
  "scripts": { "build": "node scripts/build.mjs", "typecheck": "tsc --noEmit" },
  "devDependencies": { "typescript": "^5.6.0", "vite": "^6.0.0" }
}
```

`scripts/build.mjs` — собирает каждую папку из `src/` (или одну: `npm run build -- hello`):

```js
import { build } from "vite";
import { readdirSync, statSync } from "node:fs";
import { resolve } from "node:path";

const root = resolve(import.meta.dirname, "..");
const only = process.argv[2];
const ids = readdirSync(resolve(root, "src"))
  .filter((d) => statSync(resolve(root, "src", d)).isDirectory() && (!only || d === only));

for (const id of ids) {
  await build({
    root: resolve(root, "src", id),
    base: "./",                                   // ВАЖНО: плагин живёт по /plugins/<id>/, пути /assets/… не сработают
    build: { outDir: resolve(root, "plugins", id), emptyOutDir: true, target: "es2022" },
    logLevel: "warn",
  });
  console.log("built plugins/" + id);
}
```

`src/hello/index.html`:

```html
<!doctype html>
<html><head><meta charset="utf-8"><title>Hello</title></head>
<body>
  <div id="app">…</div>
  <script src="/plugin-sdk/oc.js"></script>      <!-- классический скрипт: Vite его не трогает -->
  <script type="module" src="./main.ts"></script>
</body></html>
```

`src/hello/main.ts` (глобальный `oc` типизирован через `types/oc.d.ts`):

```ts
import "./style.css";

const app = document.getElementById("app")!;
const info = await oc.ready;
app.style.color = oc.param("color", "#ffffff");
await oc.chat.subscribe();
oc.chat.on("message", (m) => { app.textContent = `${m.displayName}: ${m.text} (${info.pluginId})`; });
```

Сборка и проверка типов: `npm i && npm run typecheck && npm run build`, затем `git add plugins` и коммит. Что нужно знать:

- **`base: "./"` обязателен.** Без него Vite пропишет `/assets/…`, а виджет открывается по `/plugins/hello/`.
- Vite напишет предупреждение `<script src="/plugin-sdk/oc.js"> can't be bundled without type="module"` — это нормально: тег остаётся как есть.
- **`target: "es2022"`** нужен для top-level `await` (OBS 28+ на Chromium 103+ его поддерживает); для более старого CEF уберите top-level `await` (обернув код в `async function main()`) и поставьте `es2020`.
- Модульные скрипты и стили загружаются в sandbox-iframe: OC отдаёт файлы плагина с `Access-Control-Allow-Origin: *`, а Vite добавляет `crossorigin`. Сборка из этого гайда проверена в sandbox-iframe в Chromium.
- Одно из ограничений sandbox — у страницы нет `localStorage`/cookie/IndexedDB и service worker (opaque origin). Данные храните через `oc.storage` (разрешение `storage`). Библиотеки, которые молча полагаются на `localStorage`, оберните в try/catch или откажитесь от них.
- Фреймворки (Preact, Svelte, Solid, Vue, React) работают как в обычном Vite-проекте; помните, что виджет в OBS — это маленькая страница, следите за размером бандла.
- Запрет на абсолютные пути касается и ваших ресурсов: шрифты и картинки кладите в `public/` или импортируйте из кода — Vite переписывает пути с учётом `base`. Внешние ресурсы (`https://…`) доступны, но если интернета у стримера нет, они не загрузятся.

### Разработка с автопересборкой

Прямо в папку плагинов OC (Add Source → Open plugins folder), без публикации:

```sh
cd src/hello
npx vite build --watch --base ./ --target es2022 --emptyOutDir --outDir "<папка plugins OC>/hello"
```

После каждого сохранения файлы пересобираются; в редакторе OC обновите виджет (удалите и добавьте слой или обновите страницу). Для отладки интерфейса
без OC можно запустить `npx vite` в `src/hello` с проксированием SDK на работающий OC (`server.proxy: { "/plugin-sdk": "http://127.0.0.1:8000" }`) —
страница откроется в обычной вкладке, настройки передаются query-параметром (`?color=%23ff0000`), но вызовы `oc.*` будут отклоняться (`oc.hosted === false`): мост работает только внутри OC.

## 5. Другие инструменты

- **Без сборки** — обычные HTML + JS (как `osu-np` в [oc-plugins](https://github.com/compico/oc-plugins)): самый простой путь, без `node_modules` и шага публикации.
- **esbuild** — один бинарник, подходит для «TypeScript → один файл»: `npx esbuild src/hello/main.ts --bundle --format=iife --target=es2022 --outfile=plugins/hello/app.js`
  (IIFE не требует `type="module"`, но top-level `await` в нём недоступен — оберните код в `async function main()`; `index.html` и `plugin.json` положите в `plugins/hello/` руками или скриптом).
- **Rollup / webpack / Parcel** — подойдут, если собирают в относительные пути (`publicPath: "./"` / `--public-url ./`).
- **Тесты**: логику без DOM и сети выносите в отдельный модуль и проверяйте `node --test` (как `plugins/osu-np/logic.js` + `test/`) или Vitest.
  Разрешения и поведение моста в тесты не нужны: подставьте объект `oc` с нужными методами.
- **Проверка манифестов**: скрипт [`scripts/validate.mjs`](https://github.com/compico/oc-plugins/blob/main/scripts/validate.mjs) из oc-plugins повторяет правила OC (id = имя папки, entry существует, типы настроек, разрешения) — скопируйте его в свой репозиторий и запускайте в CI.

## 6. CI: следите, чтобы собранное совпадало с исходниками

Магазин берёт готовые файлы, поэтому забытая пересборка — самая частая ошибка. Пример `.github/workflows/check.yml`:

```yaml
name: check
on: [push, pull_request]
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-node@v4
        with: { node-version: 22 }
      - run: npm ci
      - run: npm run typecheck
      - run: npm run build
      - run: git diff --exit-code plugins   # собранные файлы в репозитории актуальны
```

Хэши в именах файлов (`assets/index-DVK3FgIe.js`) детерминированы при тех же версиях зависимостей, поэтому закрепляйте их `package-lock.json` и используйте `npm ci`.

## 7. Публикация

1. Поднимите `version` в `plugin.json` (источник правды — манифест в `plugins/<id>/`, у Vite-проекта он лежит в `src/<id>/public/plugin.json`).
2. Соберите, закоммитьте и запушьте. Для стабильных выпусков ставьте тег: `git tag v1.2.0 && git push --tags`.
3. В OC: Add Source → Plugin store… → поле источника: `owner/repo` (всегда последняя версия ветки по умолчанию) или `owner/repo@v1.2.0` (закреплённая версия).
4. Опишите плагин в `README.md` рядом с ним: что делает, какие разрешения запрашивает и зачем, какие настройки, как проверить.

Пользователь сам отвечает за выбранные источники и видит запрашиваемые права до установки — просите только те разрешения, которые нужны плагину.
