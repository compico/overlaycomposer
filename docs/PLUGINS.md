# Плагины-виджеты

Плагин — папка с `plugin.json` и файлами виджета (HTML/JS/CSS/картинки/шрифты). Приложение раздаёт её по `/plugins/<id>/…`,
показывает виджет в iframe и строит в редакторе форму настроек по схеме из манифеста. Значения настроек виджет получает
**в query-параметрах** своего адреса: `/plugins/twitch-chat/?channel=shroud&msgSize=32`.

Куда класть: папка `plugins/` рядом с данными приложения (кнопка **Open plugins folder** в диалоге Add Source), по одной подпапке
на плагин. Перезапуск не нужен — список обновляется при открытии диалога Add Source. Встроенные плагины (например, `twitch-chat`)
зашиты в бинарник и не переопределяются.

```
plugins/
  my-widget/
    plugin.json
    index.html
    app.js
```

## plugin.json

```json
{
  "id": "my-widget",
  "name": "My widget",
  "version": "1.0.0",
  "description": "Что делает виджет",
  "entry": "index.html",
  "defaultSize": { "width": 480, "height": 200 },
  "settings": [
    { "key": "host", "label": "tosu host", "type": "string", "group": "Connection", "default": "127.0.0.1:24050" },
    { "key": "size", "label": "Font size", "type": "number", "default": 32, "min": 8, "max": 200 },
    { "key": "color", "label": "Color", "type": "color", "default": "#ffffff" },
    { "key": "mode", "label": "Mode", "type": "select", "default": "a",
      "options": [ { "value": "a", "label": "Mode A" }, { "value": "b", "label": "Mode B" } ] }
  ]
}
```

- `id` — `a-z A-Z 0-9 _ -`, до 64 символов; совпадающий с уже занятым (встроенным) id отвергается.
- `entry` — файл, который открывается по `/plugins/<id>/` (по умолчанию `index.html`).
- `defaultSize` — размер слоя при добавлении на layout.
- Типы настроек: `string`, `text` (многострочный), `number`, `range` (ползунок), `bool`, `color` (`#rrggbb`), `font` (имя шрифта, с подсказками), `select` (нужны `options`).
  `min`/`max`/`step` — для `number` и `range`; `group` — заголовок секции; `help` — подпись под полем.
- Редактор сохраняет **только изменённые** значения (кнопка ↺ возвращает к умолчанию), поэтому **значения по умолчанию должен знать и сам виджет**
  — они в манифесте нужны для формы, а в коде виджета — для запуска без параметра. Держите их одинаковыми.
- Значения в query: строка как есть, число — `32`, bool — `true`/`false`.

Плагин с ошибкой в манифесте пропускается, причина пишется в лог (`plugins: плагин пропущен`).

## Как читать настройки

```js
const q = new URLSearchParams(location.search);
const size = parseFloat(q.get("size")) || 32;
const on = q.get("showEmotes") !== "false";
```

## Ограничения (sandbox)

Виджет — чужой код, поэтому его iframe запускается с `sandbox="allow-scripts allow-forms allow-popups allow-modals"` **без** `allow-same-origin`:
- у страницы «непрозрачный» origin: нет доступа к API приложения, `localStorage`/cookie недоступны;
- `WebSocket` и `fetch` к внешним адресам работают (для `fetch` сервер должен отдавать CORS — например, tosu отдаёт), в том числе к `ws://127.0.0.1:24050` (tosu);
- свои файлы плагина (`/plugins/<id>/…`) отдаются с `Access-Control-Allow-Origin: *`, их можно загружать и `fetch`-ем.

Плагины запускают произвольный код в вашем OBS-оверлее — ставьте только те, которым доверяете.

## Идеи на будущее

Плагины с серверной частью (wasm, например через wazero), «живое» применение настроек без перезагрузки iframe (`postMessage`),
пакеты плагинов (`.zip`) — см. `TODO.md`.
