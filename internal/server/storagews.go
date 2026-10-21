package server

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/gofiber/fiber/v3"

	"github.com/compico/overlaycomposer/internal/hub"
	"github.com/compico/overlaycomposer/internal/kv"
)

// Хранилище плагинов по общему /ws. Имя плагина указывает страница оверлея (она знает, чей iframe вызывает; плагин
// подменить его не может — мост берёт id из регистрации iframe), поэтому плагин видит только свои данные.

type storageReq struct {
	Type   string          `json:"type"`
	ReqID  int             `json:"reqId"`
	Plugin string          `json:"plugin"`
	Key    string          `json:"key"`
	Value  json.RawMessage `json:"value"`
	IfRev  *int64          `json:"ifRev"`
	Prefix string          `json:"prefix"`
	Limit  int             `json:"limit"`
}

type storageRes struct {
	Type    string          `json:"type"`
	ReqID   int             `json:"reqId"`
	OK      bool            `json:"ok"`
	Found   *bool           `json:"found,omitempty"`
	Value   json.RawMessage `json:"value,omitempty"`
	Rev     *int64          `json:"rev,omitempty"`
	Deleted *bool           `json:"deleted,omitempty"`
	Entries []kv.Entry      `json:"entries,omitempty"`
	Error   string          `json:"error,omitempty"`
	Code    string          `json:"code,omitempty"` // conflict | quota | invalid | internal
}

type storageChanged struct {
	Type    string          `json:"type"`
	Plugin  string          `json:"plugin"`
	Key     string          `json:"key"`
	Deleted bool            `json:"deleted"`
	Value   json.RawMessage `json:"value,omitempty"`
	Rev     int64           `json:"rev"`
}

func (s *Server) storageWS() {
	s.d.KV.OnChange = func(c kv.Change) {
		s.d.Hub.Broadcast(storageChanged{Type: "storage:changed", Plugin: c.Plugin, Key: c.Key, Deleted: c.Deleted, Value: c.Value, Rev: c.Rev})
	}
	s.d.Hub.Handle("storage:", func(p *hub.Peer, raw []byte) {
		var m storageReq
		if json.Unmarshal(raw, &m) != nil {
			return
		}
		res := storageRes{Type: "storage:result", ReqID: m.ReqID, OK: true}
		var err error
		switch m.Type {
		case "storage:get":
			var e kv.Entry
			var found bool
			e, found, err = s.d.KV.Get(m.Plugin, m.Key)
			res.Found = &found
			res.Value, res.Rev = e.Value, &e.Rev
		case "storage:set":
			var rev int64
			rev, err = s.d.KV.Set(m.Plugin, m.Key, m.Value, m.IfRev)
			res.Rev = &rev
		case "storage:delete":
			var ok bool
			ok, err = s.d.KV.Delete(m.Plugin, m.Key)
			res.Deleted = &ok
		case "storage:list":
			res.Entries, err = s.d.KV.List(m.Plugin, m.Prefix, m.Limit)
		default:
			return
		}
		if err != nil {
			res = storageRes{Type: "storage:result", ReqID: m.ReqID, Error: err.Error(), Code: kvErrCode(err)}
		}
		p.Send(res)
	})
}

func kvErrCode(err error) string {
	switch {
	case errors.Is(err, kv.ErrConflict):
		return "conflict"
	case errors.Is(err, kv.ErrQuota):
		return "quota"
	case errors.Is(err, kv.ErrBadKey), errors.Is(err, kv.ErrBadValue), errors.Is(err, kv.ErrBadPlug):
		return "invalid"
	}
	return "internal"
}

// Данные плагина в редакторе: сколько занято и кнопка «стереть». Только с этого компьютера.
func (s *Server) storageRoutes() {
	s.app.Get("/api/plugins/:id/storage", s.localOnly, func(c fiber.Ctx) error {
		st, err := s.d.KV.Stats(c.Params("id"))
		if errors.Is(err, kv.ErrBadPlug) {
			return bad(c, err.Error())
		}
		if err != nil {
			return serverErr(c, err)
		}
		return c.JSON(st)
	})
	s.app.Delete("/api/plugins/:id/storage", s.localOnly, s.appGuard, func(c fiber.Ctx) error {
		err := s.d.KV.Clear(c.Params("id"))
		if errors.Is(err, kv.ErrBadPlug) {
			return bad(c, err.Error())
		}
		if err != nil {
			return serverErr(c, err)
		}
		return c.Status(http.StatusOK).JSON(fiber.Map{"ok": true})
	})
}
