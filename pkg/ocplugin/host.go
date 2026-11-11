package ocplugin

import (
	"encoding/json"
	"errors"
	"time"
)

// host — вызов операции хоста: операция и JSON-аргумент → JSON-ответ. В wasm его задаёт ABI, в тестах — SetHost.
var host func(op string, arg []byte) []byte

// SetHost подменяет связь с хостом (для тестов плагина на обычном `go test`). В wasm не нужен.
func SetHost(h func(op string, arg []byte) []byte) { host = h }

var errNoHost = errors.New("ocplugin: host is not available (the plugin runs outside Overlay Composer)")

func call(op string, arg any) (json.RawMessage, error) {
	if host == nil {
		return nil, errNoHost
	}
	b, err := json.Marshal(arg)
	if err != nil {
		return nil, err
	}
	out := host(op, b)
	if len(out) == 0 {
		return nil, nil
	}
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(out, &e) == nil && e.Error != "" {
		return nil, errors.New(e.Error)
	}
	return out, nil
}

// ---- лог ----

func logAt(level, msg string, kv []any) {
	if host == nil {
		return
	}
	fields := map[string]any{}
	for i := 0; i+1 < len(kv); i += 2 {
		if k, ok := kv[i].(string); ok {
			fields[k] = kv[i+1]
		}
	}
	_, _ = call("log", map[string]any{"level": level, "msg": msg, "fields": fields})
}

// Debug/Info/Warn/Error пишут в лог приложения с пометкой плагина; kv — пары «ключ, значение».
func Debug(msg string, kv ...any) { logAt("debug", msg, kv) }
func Info(msg string, kv ...any)  { logAt("info", msg, kv) }
func Warn(msg string, kv ...any)  { logAt("warn", msg, kv) }
func Error(msg string, kv ...any) { logAt("error", msg, kv) }

// ---- хранилище (PermStorage) ----

// Load читает значение по ключу. found == false — ключа нет.
func Load[T any](key string) (v T, found bool, err error) {
	out, err := call("storage.get", map[string]string{"key": key})
	if err != nil {
		return v, false, err
	}
	var r struct {
		Found bool            `json:"found"`
		Value json.RawMessage `json:"value"`
	}
	if err := json.Unmarshal(out, &r); err != nil {
		return v, false, err
	}
	if !r.Found {
		return v, false, nil
	}
	if err := json.Unmarshal(r.Value, &v); err != nil {
		return v, false, err
	}
	return v, true, nil
}

// Store записывает значение (оно должно кодироваться в JSON).
func Store(key string, v any) error {
	val, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = call("storage.set", map[string]any{"key": key, "value": json.RawMessage(val)})
	return err
}

// Remove удаляет ключ.
func Remove(key string) error {
	_, err := call("storage.delete", map[string]string{"key": key})
	return err
}

// ---- таймеры (PermTimer) ----

// SetTimer запускает таймер хоста (50 мс … 24 ч); срабатывание придёт в OnTimer(id). Тот же id заменяет таймер.
func SetTimer(id string, d time.Duration, repeat bool) error {
	_, err := call("timer.set", map[string]any{"id": id, "ms": d.Milliseconds(), "repeat": repeat})
	return err
}

// ClearTimer останавливает таймер.
func ClearTimer(id string) { _, _ = call("timer.clear", map[string]string{"id": id}) }

// ---- события (PermPublish) ----

// Publish публикует событие в топик, объявленный в Meta.Events: его получают подписчики (виджеты, аддоны, deck).
// Данные должны кодироваться в JSON (до 64 КБ). Частота ограничена хостом; при превышении вернётся ошибка.
func Publish(topic string, v any) error { return publish(topic, v, false) }

// PublishRetained публикует событие и просит хост хранить последнее значение топика: новый подписчик получит его сразу.
// Подходит для состояния («сейчас играет»), а не для команд.
func PublishRetained(topic string, v any) error { return publish(topic, v, true) }

func publish(topic string, v any, retain bool) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = call("events.publish", map[string]any{"topic": topic, "data": json.RawMessage(data), "retain": retain})
	return err
}

// ---- чат (PermChat) ----

// Say пишет в чат канала стримера от имени бота. Канал и аккаунт выбирает хост, текст — до 300 символов; частота ограничена
// (всплеск 3 сообщения, затем одно в 3 секунды). Отправка идёт в фоне: nil означает, что хост принял сообщение, а не что оно
// дошло (нет аккаунта бота, нет связи с чатом — это попадёт в лог OC).
func Say(text string) error {
	_, err := call("chat.send", map[string]any{"text": text})
	return err
}

// ---- сеть (PermNetHTTP) ----

// FetchRequest — исходящий HTTP-запрос. В локальную сеть ходить нельзя.
type FetchRequest struct {
	Method string `json:"method,omitempty"` // по умолчанию GET
	URL    string `json:"url"`
	Body   string `json:"body,omitempty"`
}

// Fetch запускает запрос и сразу возвращается; ответ придёт в OnHTTPResponse(id).
func Fetch(id string, r FetchRequest) error {
	_, err := call("http.fetch", map[string]any{"id": id, "method": r.Method, "url": r.URL, "body": r.Body})
	return err
}

// ---- награды Twitch (PermRedeem) ----

func setRedemption(rewardID, id, status string) error {
	_, err := call("twitch.redemption.update", map[string]string{"rewardId": rewardID, "id": id, "status": status})
	return err
}

// FulfillRedemption отмечает погашение выполненным.
func FulfillRedemption(rewardID, id string) error { return setRedemption(rewardID, id, "FULFILLED") }

// CancelRedemption отменяет погашение, баллы возвращаются зрителю.
func CancelRedemption(rewardID, id string) error { return setRedemption(rewardID, id, "CANCELED") }
