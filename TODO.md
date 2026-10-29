# TODO

Задачи ведутся в GitHub Issues: <https://github.com/compico/overlaycomposer/issues>

Приоритеты задаются метками:

- [`P1: high`](https://github.com/compico/overlaycomposer/labels/P1%3A%20high) — безопасность, потеря данных, ломает использование;
- [`P2: medium`](https://github.com/compico/overlaycomposer/labels/P2%3A%20medium) — важно, но не блокирует;
- [`P3: low`](https://github.com/compico/overlaycomposer/labels/P3%3A%20low) — улучшения и идеи на потом.

Области: `area: plugins`, `area: twitch`, `area: proxy`, `area: editor`, `area: obs`, `area: infra`, `area: security`, `area: remote`.

## P1

- #39 пароль obs-websocket в `config.json` открытым текстом
- #40 авторизация при host != 127.0.0.1, проверка Origin у WS, лимиты
- #41 миграции БД (`schemaVersion`)
- #42 оптимистичная блокировка `rev` при сохранении из двух вкладок
- #43 прокси: одинаковый путь ассета у двух источников
- #44 `net.local`: белый список портов/путей, rate limit

## P2

Прокси: #35, #36, #63, #64, #65, #66, #67 ·
Twitch: #45–#51 ·
Плагины: #52–#60 ·
Сцены/OBS: #61, #62, #68, #69, #71 ·
Редактор/UI: #70, #73, #74 ·
Инфраструктура: #72

## P3

#75, #76 (доступ извне) · #77 (osu!) · #78 (wasm) · #79 (CEF) · #80–#82 (продукт) · #83 (релиз)
