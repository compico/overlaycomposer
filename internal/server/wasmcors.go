package server

import (
	"strings"

	"github.com/gofiber/fiber/v3"
)

// trustedPluginOrigins — страницы, которым разрешено обращаться к HTTP-API плагинов (/plugins/<id>/api/*) с записью.
//
// Аддоны PulseSync выполняются внутри окна Яндекс Музыки и ходят в OC обычным fetch с origin music-application://desktop:
// это настоящий браузерный запрос, поэтому ему нужен CORS (preflight OPTIONS), а originGuard без исключения отвечал бы 403
// на любой POST с чужим Origin. Origin с такой схемой браузер не даёт подделать странице сайта: веб-страница не может его
// выставить, а другое приложение на этом компьютере и так могло бы отправить запрос без Origin вовсе. Поэтому доверие
// ограничено именно этим origin и только маршрутами плагинов; остальной /api/*, WebSocket и страницы приложения не затронуты.
var trustedPluginOrigins = []string{"music-application://desktop"}

// pluginAPIPath: /plugins/<id>/api и /plugins/<id>/api/… (но не /plugins/<id>/apix, не /plugins/<id>/ui/api).
func pluginAPIPath(p string) bool {
	rest, ok := strings.CutPrefix(p, "/plugins/")
	if !ok {
		return false
	}
	id, tail, ok := strings.Cut(rest, "/")
	if !ok || id == "" {
		return false
	}
	return tail == "api" || strings.HasPrefix(tail, "api/")
}

func trustedPluginOrigin(o string) bool {
	for _, t := range trustedPluginOrigins {
		if o == t {
			return true
		}
	}
	return false
}

const pluginOriginTrusted = "oc.trusted-plugin-origin"

// pluginAPIOrigin — CORS для доверенных страниц на маршрутах плагинов. Ставится раньше originGuard: тот пропускает запрос,
// помеченный здесь. Preflight (OPTIONS) отвечает сам, до плагина он не доходит. Для остальных origin и путей ничего не меняется.
func (s *Server) pluginAPIOrigin(c fiber.Ctx) error {
	o := c.Get("Origin")
	if o == "" || !trustedPluginOrigin(o) || !pluginAPIPath(c.Path()) {
		return c.Next()
	}
	c.Vary("Origin")
	c.Set("Access-Control-Allow-Origin", o)
	c.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
	c.Set("Access-Control-Allow-Headers", "Content-Type")
	c.Set("Access-Control-Max-Age", "600")
	if c.Method() == fiber.MethodOptions {
		return c.SendStatus(fiber.StatusNoContent)
	}
	c.Locals(pluginOriginTrusted, true)
	return c.Next()
}
