package ocplugin

import (
	"encoding/json"
	"errors"
)

// Ctx — запрос к маршруту и ответ на него.
type Ctx struct {
	Method string
	Path   string

	query    map[string]string
	body     json.RawMessage
	status   int
	respBody json.RawMessage
}

// Query возвращает параметр строки запроса ("" — нет такого).
func (c *Ctx) Query(key string) string { return c.query[key] }

// Bind разбирает JSON-тело запроса в v. Пустое тело — ошибка.
func (c *Ctx) Bind(v any) error {
	if len(c.body) == 0 {
		return errors.New("empty request body")
	}
	return json.Unmarshal(c.body, v)
}

// JSON отвечает значением v как JSON-телом с указанным статусом.
func (c *Ctx) JSON(status int, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.status, c.respBody = status, b
	return nil
}

// NoContent отвечает 204 без тела.
func (c *Ctx) NoContent() error { c.status, c.respBody = 204, nil; return nil }

// Error отвечает {"error": msg} с указанным статусом. Сообщение увидит вызывающий — не кладите в него внутренние детали.
func (c *Ctx) Error(status int, msg string) error {
	return c.JSON(status, map[string]string{"error": msg})
}
