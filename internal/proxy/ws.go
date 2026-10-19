package proxy

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/fasthttp/websocket"
	fws "github.com/gofiber/contrib/v3/websocket"
	"github.com/gofiber/fiber/v3"
)

// Секреты из кадров Centrifugo (токены, подписи) маскируем, остальное — как есть.
var secretRe = regexp.MustCompile(`("(?:token|sign|b64info)"\s*:\s*")[^"]*(")`)

// handleWS: /proxy-ws/<id>/<ws|wss>/<host>/<путь>. Разрешены только хосты того же «сайта», что и источник.
// Соединение с сайтом открывается ДО апгрейда клиента: так выбранный сайтом подпротокол можно вернуть странице,
// а кадры не нужно накапливать в очереди.
func (p *Proxy) handleWS(c fiber.Ctx) error {
	id, tail, ok := splitID(strings.Clone(c.OriginalURL()), WSPrefix) // копия: websocket-обработчик живёт дольше запроса, а буфер Fiber переиспользуется
	if !ok {
		return text(c, 400, "bad request")
	}
	origin := UpstreamOrigin(p.lookup(id))
	if origin == "" {
		return text(c, 404, "not found")
	}
	m := hostTailRe.FindStringSubmatch(tail)
	if m == nil || (m[1] != "ws" && m[1] != "wss") {
		return text(c, 400, "bad ws path")
	}
	path := m[3]
	if path == "" {
		path = "/"
	}
	u, err := url.Parse(m[1] + "://" + m[2] + path + m[4])
	if err != nil || !strings.EqualFold(u.Host, m[2]) {
		return text(c, 400, "bad url")
	}
	src, _ := url.Parse(origin)
	site := SiteOf(strings.ToLower(src.Hostname()))
	if !hostInSite(strings.ToLower(u.Hostname()), site) {
		slog.Warn("proxy-ws: хост не относится к сайту источника — соединение отклонено", "host", u.Hostname(), "site", site)
		return text(c, 403, "host not allowed")
	}

	var offered []string
	for _, s := range strings.Split(c.Get("Sec-WebSocket-Protocol"), ",") {
		if s = strings.TrimSpace(s); s != "" {
			offered = append(offered, s)
		}
	}
	hdr := http.Header{"Origin": {origin}}
	if ua := c.Get("User-Agent"); ua != "" {
		hdr.Set("User-Agent", ua)
	}
	d := websocket.Dialer{HandshakeTimeout: 20 * time.Second, Subprotocols: offered, Proxy: http.ProxyFromEnvironment}
	up, resp, err := d.Dial(u.String(), hdr)
	label := u.Host + u.Path // без query — там могут быть токены
	if err != nil {
		status := ""
		if resp != nil {
			status = fmt.Sprint(resp.StatusCode)
		}
		slog.Warn("proxy-ws: не удалось открыть", "target", label, "status", status, "err", err)
		return text(c, 502, "cannot open websocket")
	}

	cfg := fws.Config{}
	if sp := up.Subprotocol(); sp != "" {
		cfg.Subprotocols = []string{sp}
	}
	debug := p.debug.Load()
	return fws.New(func(client *fws.Conn) {
		start := time.Now()
		slog.Info("proxy-ws: открыт", "target", label)
		done := make(chan struct{}, 2)
		go func() { pump(client.Conn, up, "page→site", u.Host, debug); done <- struct{}{} }()
		go func() { pump(up, client.Conn, "site→page", u.Host, debug); done <- struct{}{} }()
		<-done
		_ = client.Close()
		_ = up.Close()
		<-done
		slog.Info("proxy-ws: закрыт", "target", label, "lived", time.Since(start).Round(time.Second))
	}, cfg)(c)
}

// pump переносит кадры src → dst (тип кадра — текст/бинарный — сохраняется); при закрытии передаёт код закрытия.
func pump(src, dst *websocket.Conn, dir, host string, debug bool) {
	for {
		mt, msg, err := src.ReadMessage()
		if err != nil {
			closeWith(dst, err)
			return
		}
		if debug && !(mt == websocket.TextMessage && string(msg) == "{}") { // {} — пинг/понг Centrifugo, шум
			if mt == websocket.TextMessage {
				s := secretRe.ReplaceAllString(string(msg), "${1}***${2}")
				if len(s) > 300 {
					s = s[:300] + "…"
				}
				slog.Info("proxy-ws кадр", "dir", dir, "host", host, "data", s)
			} else {
				slog.Info("proxy-ws кадр", "dir", dir, "host", host, "binary", len(msg))
			}
		}
		if err := dst.WriteMessage(mt, msg); err != nil {
			return
		}
	}
}

// closeWith шлёт dst кадр закрытия с кодом, пришедшим от src (только допустимые для отправки коды).
func closeWith(dst *websocket.Conn, err error) {
	code, reason := websocket.CloseNormalClosure, ""
	if ce, ok := errors.AsType[*websocket.CloseError](err); ok {
		switch {
		case ce.Code >= 1000 && ce.Code <= 1003, ce.Code >= 1007 && ce.Code <= 1011, ce.Code >= 3000 && ce.Code <= 4999:
			code, reason = ce.Code, ce.Text
		}
	}
	_ = dst.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(code, reason), time.Now().Add(time.Second))
}
