package server

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/compico/overlaycomposer/internal/hub"
	"github.com/compico/overlaycomposer/internal/localnet"
)

// Запросы плагинов к локальным программам (разрешение net.local проверяет страница оверлея, а адрес — localnet).

type netReq struct {
	Type   string `json:"type"`
	ReqID  int    `json:"reqId"`
	Target string `json:"target"`
	Path   string `json:"path"`
}

type netRes struct {
	Type   string `json:"type"`
	ReqID  int    `json:"reqId"`
	OK     bool   `json:"ok"`
	Status int    `json:"status,omitempty"`
	Body   string `json:"body,omitempty"`
	Error  string `json:"error,omitempty"`
}

func (s *Server) netWS() {
	s.d.Hub.Handle("net:", func(p *hub.Peer, raw []byte) {
		var m netReq
		if json.Unmarshal(raw, &m) != nil || m.Type != "net:get" {
			return
		}
		go func() { // сеть — не блокируем чтение сообщений клиента
			res := netRes{Type: "net:result", ReqID: m.ReqID, OK: true}
			r, err := localnet.Get(context.Background(), m.Target, m.Path)
			if err != nil {
				res.OK, res.Error = false, err.Error()
				slog.Debug("net.local: запрос не удался", "target", m.Target, "err", err)
			} else {
				res.Status, res.Body = r.Status, r.Body
			}
			p.Send(res)
		}()
	})
}
