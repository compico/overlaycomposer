package server

import (
	"net"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"

	"github.com/compico/overlaycomposer/internal/auth"
)

const tokenCookie = "oc_token"

// remoteIP — адрес, с которого пришло TCP-соединение.
func remoteIP(c fiber.Ctx) string {
	addr := c.RequestCtx().RemoteAddr()
	if addr == nil {
		return ""
	}
	h, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return addr.String()
	}
	return h
}

// isLocal — запрос с этого же компьютера: loopback-соединение без следов обратного прокси
// (туннель/прокси на localhost иначе открыл бы доступ всему интернету).
func isLocal(c fiber.Ctx) bool {
	ip := net.ParseIP(remoteIP(c))
	if ip == nil || !ip.IsLoopback() {
		return false
	}
	for _, h := range []string{"X-Forwarded-For", "X-Forwarded-Host", "X-Real-Ip", "Forwarded", "Cf-Connecting-Ip"} {
		if c.Get(h) != "" {
			return false
		}
	}
	return true
}

func tokenFrom(c fiber.Ctx) string {
	if h := c.Get("Authorization"); len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return c.Cookies(tokenCookie)
}

// authGuard пропускает локальные запросы и запросы с действующим токеном устройства. Файлы плагинов открыты:
// их iframe в sandbox имеет «непрозрачный» origin, и cookie к его подзапросам не прикладывается (файлы публичны).
func (s *Server) authGuard(c fiber.Ctx) error {
	if isLocal(c) {
		return c.Next()
	}
	p := c.Path()
	if p == "/pair" || p == "/api/pair" || strings.HasPrefix(p, "/plugins/") || strings.HasPrefix(p, "/plugin-sdk/") {
		return c.Next()
	}
	if t := tokenFrom(c); t != "" {
		if _, err := s.d.Auth.Verify(t, remoteIP(c)); err == nil {
			return c.Next()
		}
	}
	if c.Method() == fiber.MethodGet && !strings.HasPrefix(p, "/api/") && p != "/ws" && strings.Contains(c.Get("Accept"), "text/html") {
		return c.Redirect().To("/pair")
	}
	return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
}

// localOnly — управление устройствами доступно только с этого компьютера, иначе сопряжённое устройство
// могло бы выдать себе подобных или увидеть код сопряжения.
func (s *Server) localOnly(c fiber.Ctx) error {
	if !isLocal(c) {
		return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "available only from this computer"})
	}
	return c.Next()
}

func (s *Server) authRoutes() {
	app := s.app
	app.Get("/pair", func(c fiber.Ctx) error {
		c.Set("Cache-Control", "no-store")
		return s.htmlString(c, pairPage)
	})
	app.Post("/api/pair", s.pair)
	app.Get("/api/devices", s.localOnly, s.listDevices)
	app.Delete("/api/devices/:id", s.localOnly, s.appGuard, s.revokeDevice)
}

func (s *Server) pair(c fiber.Ctx) error {
	var b struct {
		Code string `json:"code"`
		Name string `json:"name"`
	}
	if !body(c, &b) {
		return bad(c, "invalid json")
	}
	name := b.Name
	if strings.TrimSpace(name) == "" {
		name = deviceNameFromUA(c.Get("User-Agent"))
	}
	token, dev, err := s.d.Auth.Pair(b.Code, name, remoteIP(c))
	switch err {
	case nil:
	case auth.ErrLocked:
		return c.Status(fiber.StatusTooManyRequests).JSON(fiber.Map{"error": err.Error()})
	case auth.ErrBadCode:
		return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": err.Error()})
	default:
		return serverErr(c, err)
	}
	c.Cookie(&fiber.Cookie{
		Name: tokenCookie, Value: token, Path: "/", HTTPOnly: true, SameSite: "Lax",
		Expires: time.Now().AddDate(10, 0, 0),
	})
	return c.JSON(fiber.Map{"token": token, "device": dev})
}

func (s *Server) listDevices(c fiber.Ctx) error {
	code, left := s.d.Auth.Pairing()
	host := net.ParseIP(s.d.Running.Host)
	return c.JSON(fiber.Map{
		"code":          code,
		"expiresIn":     int(left.Seconds()),
		"devices":       s.d.Auth.Devices(),
		"remoteEnabled": host == nil || !host.IsLoopback(),
		"pairUrl":       s.baseURL() + "/pair",
	})
}

func (s *Server) revokeDevice(c fiber.Ctx) error {
	ok, err := s.d.Auth.Revoke(c.Params("id"))
	if err != nil {
		return serverErr(c, err)
	}
	if !ok {
		return notFound(c)
	}
	return c.JSON(fiber.Map{"ok": true})
}

func deviceNameFromUA(ua string) string {
	for _, k := range []string{"iPad", "iPhone", "Android", "Windows", "Macintosh", "Linux"} {
		if strings.Contains(ua, k) {
			return k
		}
	}
	return "Device"
}

// pairPage — страница ввода кода. Только ES5 и XMLHttpRequest, чтобы открывалась на очень старых браузерах (iPad с Safari 9).
const pairPage = `<!doctype html>
<html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Overlay Composer — connect</title>
<style>
body{margin:0;background:#15171c;color:#e6e8ee;font-family:-apple-system,Helvetica,Arial,sans-serif;text-align:center;padding:48px 16px}
h1{font-size:20px;font-weight:600}p{color:#9aa3b2;font-size:14px;line-height:1.4}
input{display:block;margin:24px auto 12px;width:240px;font-size:32px;letter-spacing:6px;text-align:center;text-transform:uppercase;
 padding:10px;border:1px solid #3a4150;border-radius:8px;background:#0e1014;color:#fff;font-family:Menlo,monospace}
button{font-size:16px;padding:10px 28px;border:0;border-radius:8px;background:#6d5efc;color:#fff}
#err{color:#ff7a7a;min-height:20px;margin-top:14px;font-size:14px}
</style></head><body>
<h1>Connect to Overlay Composer</h1>
<p>Open Settings on the computer running Overlay Composer<br>and enter the 6-letter code shown under “Remote devices”.</p>
<input id="code" maxlength="6" autocomplete="off" autocapitalize="characters" autocorrect="off" spellcheck="false">
<button id="go">Connect</button>
<div id="err"></div>
<script>
(function(){
var inp=document.getElementById("code"),err=document.getElementById("err");
function go(){
 var code=inp.value.replace(/\s/g,"");
 if(code.length!==6){err.textContent="Enter the 6-letter code";return;}
 err.textContent="";
 var x=new XMLHttpRequest();x.open("POST","/api/pair");x.setRequestHeader("Content-Type","application/json");
 x.onload=function(){
  if(x.status===200){location.href="/editor";return;}
  var m="Error "+x.status;try{m=JSON.parse(x.responseText).error||m;}catch(e){}
  err.textContent=m;
 };
 x.onerror=function(){err.textContent="No connection";};
 x.send(JSON.stringify({code:code}));
}
document.getElementById("go").onclick=go;
inp.onkeydown=function(e){if((e.keyCode||e.which)===13)go();};
})();
</script></body></html>`
