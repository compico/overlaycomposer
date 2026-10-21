/**
 * Overlay Composer — API для плагинов-виджетов (версия 1).
 *
 * Подключение в index.html плагина:
 *
 *   <script src="/plugin-sdk/oc.js"></script>
 *
 * После этого доступен глобальный объект `oc` (типы — этот файл, он же отдаётся по /plugin-sdk/oc.d.ts).
 * В TypeScript-проекте положите файл рядом и добавьте `/// <reference path="oc.d.ts" />`.
 *
 * Как это работает: виджет живёт в изолированном iframe (sandbox, без доступа к API приложения). Все вызовы идут через
 * страницу оверлея по postMessage; чат Twitch OC читает одним общим соединением и раздаёт всем плагинам, а сообщения
 * отправляет сам от имени аккаунта бота/стримера — токены плагину не передаются. Каждый вызов к чату требует
 * разрешения, объявленного в plugin.json (`"permissions": ["chat.read", "chat.send"]`) и выданного пользователем
 * в редакторе при добавлении виджета. Без разрешения вызов отклоняется с ошибкой `permission denied: <имя>`.
 *
 * Все методы возвращают Promise и могут отклониться с `Error`.
 */

/** Разрешения плагина. */
type OCPermission =
  /** Читать чат Twitch (chat.subscribe и события chat.on). */
  | "chat.read"
  /** Писать в чат Twitch от имени подключённого аккаунта (chat.say). */
  | "chat.send";

/** Сведения о виджете, которые OC сообщает плагину после запуска. */
interface OCPluginInfo {
  /** id плагина (plugin.json → id). */
  pluginId: string;
  /** id источника (source) в OC: настройки и разрешения принадлежат ему. */
  sourceId: string;
  /** Сохранённые значения настроек из редактора (то же, что приходит в query-параметрах адреса). */
  settings: Record<string, string | number | boolean>;
  /** Разрешения, выданные этому виджету. */
  permissions: OCPermission[];
  /** true — виджет показан в предпросмотре редактора, а не в оверлее: отправка в чат отключена. */
  preview: boolean;
}

/** Смайл в тексте сообщения. start/end — позиции символов (code points) в `text`, end включительно. */
interface OCChatEmote {
  /** id смайла Twitch: картинка https://static-cdn.jtvnw.net/emoticons/v2/<id>/default/dark/2.0 */
  id: string;
  start: number;
  end: number;
}

/** Сообщение чата. */
interface OCChatMessage {
  /** id сообщения (его же передают в `say({ replyTo })`). */
  id: string;
  /** Канал в нижнем регистре, без «#». */
  channel: string;
  userId: string;
  /** Ник в нижнем регистре. */
  login: string;
  displayName: string;
  /** Цвет ника «#RRGGBB» или "" — если пользователь его не выбирал. */
  color: string;
  /** Текст без служебной обёртки /me. */
  text: string;
  /** Сообщение написано командой /me. */
  action: boolean;
  /** Имена значков: broadcaster, moderator, vip, subscriber, … */
  badges: string[];
  isBroadcaster: boolean;
  isMod: boolean;
  isVip: boolean;
  isSub: boolean;
  /** Первое сообщение пользователя в этом чате. */
  firstMsg: boolean;
  emotes: OCChatEmote[];
  /** id сообщения, на которое это ответ (если ответ). */
  replyTo?: string;
  /** Время отправки, мс (Unix epoch). */
  timestamp: number;
  /** Написано подключённым аккаунтом бота: так плагин не отвечает сам себе. */
  fromBot: boolean;
}

interface OCChatDelete {
  channel: string;
  /** id удалённого сообщения. */
  targetId: string;
  /** Автор сообщения. */
  login: string;
}

interface OCChatClear {
  channel: string;
  /** Если не пусто — удалены (timeout/ban) все сообщения этого пользователя, иначе очищен весь чат. */
  login: string;
}

interface OCChatStatus {
  /** Есть ли соединение OC с чатом Twitch. */
  connected: boolean;
  error?: string;
}

interface OCChatEvents {
  message: OCChatMessage;
  delete: OCChatDelete;
  clear: OCChatClear;
  status: OCChatStatus;
}

interface OCChatSubscribeOptions {
  /**
   * Канал Twitch. По умолчанию — канал подключённого аккаунта Стримера
   * (если он не подключён, подписка завершится ошибкой).
   */
  channel?: string;
}

interface OCChatSayOptions {
  /** Канал, по умолчанию — канал Стримера. */
  channel?: string;
  /**
   * От имени какого аккаунта писать: "bot" или "streamer".
   * По умолчанию — бот, а если он не подключён, то стример.
   */
  as?: "bot" | "streamer";
  /**
   * id сообщения, на которое отвечаем (`message.id`): Twitch покажет ответ в ветке.
   * Одинаковый ответ на одно и то же сообщение отправляется один раз, даже если оверлей открыт в двух местах.
   */
  replyTo?: string;
}

interface OCChat {
  /**
   * Подписаться на чат канала. Нужно разрешение `chat.read`.
   * Возвращает настоящее имя канала. Подписка снимается сама, когда виджет закрывается.
   */
  subscribe(options?: OCChatSubscribeOptions): Promise<{ channel: string }>;
  /** Снять подписку, сделанную через subscribe. */
  unsubscribe(channel: string): Promise<void>;
  /**
   * Подписаться на событие. Возвращает функцию отписки. События приходят только по каналам, на которые вы подписались.
   *
   *   oc.chat.on("message", (m) => { if (m.text === "!hi") oc.chat.say("Hello!", { replyTo: m.id }); });
   *   await oc.chat.subscribe();
   */
  on<K extends keyof OCChatEvents>(event: K, handler: (data: OCChatEvents[K]) => void): () => void;
  off<K extends keyof OCChatEvents>(event: K, handler: (data: OCChatEvents[K]) => void): void;
  /**
   * Написать в чат. Нужно разрешение `chat.send`. До 500 символов; не работает в предпросмотре редактора.
   * Промис выполняется, когда сообщение передано Twitch (не гарантирует, что Twitch его принял:
   * например, из-за режима чата только для подписчиков). Если слишком часто — отклоняется (лимит Twitch).
   */
  say(text: string, options?: OCChatSayOptions): Promise<void>;
}

interface OC {
  /** Версия протокола API. */
  readonly version: 1;
  /** true — страница открыта внутри OC; false — сама по себе (тогда вызовы API отклоняются). */
  readonly hosted: boolean;
  /** Выполняется, когда OC ответил; содержит сведения о виджете. Отклоняется, если виджет открыт вне OC. */
  readonly ready: Promise<OCPluginInfo>;
  /**
   * Значение настройки из query-параметров адреса, приведённое к типу значения по умолчанию.
   * Работает и внутри OC, и при открытии страницы напрямую: `oc.param("msgSize", 28)`.
   * Для boolean значения "true"/"1" дают true, остальные — false.
   */
  param<T extends string | number | boolean>(key: string, fallback: T): T;
  readonly chat: OCChat;
}

declare const oc: OC;
