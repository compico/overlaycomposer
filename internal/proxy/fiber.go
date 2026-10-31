package proxy

import (
	"bufio"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/gofiber/fiber/v3"
)

// Register вешает на приложение маршруты прокси и обработчик «запросов со страниц, загруженных через прокси».
// Вызывать до остальных маршрутов.
func (p *Proxy) Register(app *fiber.App) {
	app.Use(p.refererFallback)
	app.All(Prefix+"*", p.handleHTTP)
	app.All(HostPrefix+"*", p.handleHost)
	app.Get(WSPrefix+"*", p.handleWS)
}

func text(c fiber.Ctx, code int, msg string) error {
	return c.Status(code).SendString(msg)
}

// refererFallback: запросы со страницы, загруженной через прокси (root-relative ассеты, fetch('/api/…')), уходят
// на origin этого же источника.
func (p *Proxy) refererFallback(c fiber.Ctx) error {
	if strings.HasPrefix(c.Path(), "/proxy") {
		return c.Next()
	}
	ref := c.Get("Referer")
	if ref == "" {
		return c.Next()
	}
	id := p.refererID(ref, c.Host())
	if id == "" {
		return c.Next()
	}
	src := p.lookup(id)
	origin := UpstreamOrigin(src)
	if origin == "" {
		return c.Next()
	}
	// Тот же путь (/assets/index.js) уже закреплён за другим источником: у зависимостей этого файла Referer — просто
	// «/assets/index.js», и по нему источник не определить. Уводим запрос под /proxy/<id>/…: адрес файла тогда несёт
	// id источника, и его зависимости находятся прямо по Referer.
	if m := c.Method(); m == fiber.MethodGet || m == fiber.MethodHead {
		if u, perr := url.ParseRequestURI(c.OriginalURL()); perr == nil {
			if owner := p.served.get(u.Path); owner != "" && owner != id {
				return c.Redirect().Status(fiber.StatusTemporaryRedirect).To(Prefix + id + c.OriginalURL())
			}
		}
	}
	err := p.forward(c, id, origin, src.Data.Router, c.OriginalURL(), nil)
	if err == nil && c.Response().StatusCode() < 400 {
		if u, perr := url.ParseRequestURI(c.OriginalURL()); perr == nil {
			p.served.set(u.Path, id) // зависимости этого ресурса придут с Referer = его URL
		}
	}
	return err
}

// /proxy/<id>/<путь>
func (p *Proxy) handleHTTP(c fiber.Ctx) error {
	id, tail, ok := splitID(c.OriginalURL(), Prefix)
	if !ok {
		return text(c, 400, "bad request")
	}
	src := p.lookup(id)
	origin := UpstreamOrigin(src)
	if origin == "" {
		return text(c, 404, "not found")
	}
	return p.forward(c, id, origin, src.Data.Router, tail, nil)
}

var hostTailRe = regexp.MustCompile(`^/(https?|wss?)/([^/?#]+)(/[^?#]*)?(\?.*)?$`)

// /proxy-h/<id>/<http|https>/<host>/<путь>
func (p *Proxy) handleHost(c fiber.Ctx) error {
	id, tail, ok := splitID(c.OriginalURL(), HostPrefix)
	if !ok {
		return text(c, 400, "bad request")
	}
	src := p.lookup(id)
	origin := UpstreamOrigin(src)
	if origin == "" {
		return text(c, 404, "not found")
	}
	m := hostTailRe.FindStringSubmatch(tail)
	if m == nil || (m[1] != "http" && m[1] != "https") {
		return text(c, 400, "bad host path")
	}
	sibling := SiblingOrigin(origin, m[1], m[2])
	if sibling == "" {
		return text(c, 403, "host not allowed")
	}
	path := m[3]
	if path == "" {
		path = "/"
	}
	via := &Via{Origin: sibling, Mount: strings.TrimRight(HostPrefix, "/") + "/" + id + "/" + m[1] + "/" + m[2]}
	return p.forward(c, id, origin, src.Data.Router, path+m[4], via)
}

// splitID разбирает "<prefix><id><tail>" из OriginalURL (путь и query сохраняются в исходной кодировке).
func splitID(original, prefix string) (id, tail string, ok bool) {
	if !strings.HasPrefix(original, prefix) {
		return "", "", false
	}
	rest := original[len(prefix):]
	end := strings.IndexAny(rest, "/?#")
	if end < 0 {
		id, tail = rest, "/"
	} else {
		id, tail = rest[:end], rest[end:]
		if tail[0] != '/' {
			tail = "/" + tail // /proxy/<id>?x=1
		}
	}
	return id, tail, id != ""
}

// forward выполняет запрос через Do и отдаёт ответ браузеру (потоком, со сбросом буфера — для SSE и long-poll).
func (p *Proxy) forward(c fiber.Ctx, id, origin, router, pathQuery string, via *Via) error {
	hdr := http.Header{}
	for k, v := range c.Request().Header.All() {
		hdr.Add(string(k), string(v))
	}
	resp, err := p.Do(Request{
		Method: c.Method(), ID: id, Origin: origin, Router: router, PathQuery: pathQuery, Header: hdr,
		Body: append([]byte(nil), c.Body()...), Via: via,
	})
	if err != nil {
		code, msg := errCode(err)
		return text(c, code, msg)
	}

	c.Status(resp.Status)
	// Не c.Append: он дописывает значение к уже стоящему по умолчанию Content-Type (text/plain) через запятую,
	// и браузер отвергает стили и модули ("MIME type text/plain"). Set перезаписывает, Add для Set-Cookie
	// сохраняет отдельные строки (Append склеил бы их в одну).
	h := &c.Response().Header
	for k, vs := range resp.Header {
		for i, v := range vs {
			if i == 0 {
				h.Set(k, v)
			} else {
				h.Add(k, v)
			}
		}
	}
	for _, ck := range resp.Cookies {
		h.Add("Set-Cookie", ck)
	}
	if resp.Data != nil {
		defer resp.Close()
		return c.Send(resp.Data)
	}
	if resp.Body == nil || c.Method() == fiber.MethodHead {
		resp.Close()
		return nil
	}
	// Всё нужное из c уже скопировано: писатель потока работает после возврата из обработчика.
	return c.SendStreamWriter(func(w *bufio.Writer) {
		defer resp.Close()
		buf := make([]byte, 32<<10)
		for {
			n, err := resp.Body.Read(buf)
			if n > 0 {
				if _, werr := w.Write(buf[:n]); werr != nil {
					return // браузер ушёл — закрываем соединение с сайтом
				}
				if werr := w.Flush(); werr != nil {
					return
				}
			}
			if err != nil {
				if err != io.EOF {
					_ = err
				}
				return
			}
		}
	})
}
