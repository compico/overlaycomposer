package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strconv"
	"time"

	"github.com/compico/overlaycomposer/internal/hub"
	"github.com/compico/overlaycomposer/internal/localnet"
	"github.com/compico/overlaycomposer/internal/plugins"
)

// Запросы плагинов к локальным программам. Страница оверлея лишь передаёт, чей это запрос (плагин и виджет); решение
// принимает сервер по своим данным: у виджета выдано разрешение net.local, а манифест плагина (netLocal) разрешает именно
// этот адрес и путь. Порт самого OC закрыт всегда (иначе плагин читал бы /api/* с правами локального пользователя).

type netReq struct {
	Type   string `json:"type"`
	ReqID  int    `json:"reqId"`
	Plugin string `json:"plugin"`
	Source string `json:"source"`
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

// netLimiter: 60 запросов за 10 с и 4 одновременных на виджет, 16 одновременных всего.
var netLimiter = localnet.NewLimiter(10*time.Second, 60, 4, 16)

// netGet проверяет запрос плагина и, если он разрешён, выполняет его.
func (s *Server) netGet(m netReq) (localnet.Result, error) {
	deny := func(msg string) (localnet.Result, error) { return localnet.Result{}, errors.New(msg) }
	if s.d.Store == nil || s.d.Plugins == nil {
		return deny("not available")
	}
	src, err := s.d.Store.GetSource(m.Source)
	if err != nil || src == nil || src.Type != "plugin" || src.Data.Plugin != m.Plugin || m.Plugin == "" {
		return deny("unknown widget")
	}
	if !slices.Contains(src.Data.Granted, plugins.PermNetLocal) {
		return deny("permission denied: net.local")
	}
	pl, ok := s.d.Plugins.Get(m.Plugin)
	if !ok {
		return deny("unknown plugin")
	}
	setting := func(key string) string {
		if v, ok := src.Data.Settings[key]; ok {
			if str, ok := v.(string); ok {
				return str
			}
			return ""
		}
		for _, st := range pl.Settings { // не менялось пользователем — действует значение по умолчанию из манифеста
			if st.Key == key {
				if str, ok := st.Default.(string); ok {
					return str
				}
			}
		}
		return ""
	}
	target, err := localnet.Allowed(pl.NetLocal, setting, m.Target, m.Path)
	if err != nil {
		return localnet.Result{}, err
	}
	if localnet.PortOf(target) == strconv.Itoa(s.d.Running.Port) {
		return localnet.Result{}, localnet.ErrOwnPort
	}
	done, err := netLimiter.Acquire(m.Source)
	if err != nil {
		return localnet.Result{}, err
	}
	defer done()
	return localnet.Get(context.Background(), target, m.Path)
}

func (s *Server) netWS() {
	s.d.Hub.Handle("net:", func(p *hub.Peer, raw []byte) {
		var m netReq
		if json.Unmarshal(raw, &m) != nil || m.Type != "net:get" {
			return
		}
		go func() { // сеть — не блокируем чтение сообщений клиента
			res := netRes{Type: "net:result", ReqID: m.ReqID, OK: true}
			r, err := s.netGet(m)
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
