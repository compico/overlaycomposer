// Package proxy — прокси для iframe-источников, запрещающих встраивание (X-Frame-Options / CSP frame-ancestors).
//
//   - /proxy/<id>/<путь> — HTTP-запрос к origin источника; из ответа убираются заголовки, запрещающие встраивание;
//     в HTML вставляется скрипт-патч, проверки «я во фрейме?» скрываются.
//   - Запросы с Referer из /proxy/<id>/… (root-relative ассеты, fetch('/api/…')) пересылаются на тот же origin.
//   - /proxy-h/<id>/<http|https>/<host>/<путь> — HTTP на соседние хосты того же «сайта» (socket8.example.com).
//   - /proxy-ws/<id>/<ws|wss>/<host>/<путь> — WebSocket на хост того же «сайта»; Origin подменяется на origin источника.
//
// Безопасность: проксируются ТОЛЬКО origin-ы из зарегистрированных source с proxy:true; произвольный URL передать
// нельзя (это не open proxy / SSRF). Ядро (Do) не зависит от Fiber и проверяется тестами на httptest-сервере.
package proxy

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/compico/overlaycomposer/internal/model"
	"github.com/compico/overlaycomposer/internal/rewrite"
)

const (
	// Сколько ждём заголовки ответа сайта. Long-poll (socket.io) держит запрос ~25 с — берём с запасом.
	headersTimeout = 120 * time.Second
	// Крупнее этого HTML/JS не переписываем, а отдаём потоком.
	maxRewriteBytes = 8 << 20
)

// Lookup возвращает источник по id (nil — нет такого).
type Lookup func(id string) *model.Source

type Proxy struct {
	lookup Lookup
	debug  atomic.Bool
	client *http.Client
	served servedPaths
}

func New(lookup Lookup) *Proxy {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.ResponseHeaderTimeout = headersTimeout
	tr.DialContext = (&net.Dialer{Timeout: 20 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	return &Proxy{
		lookup: lookup,
		client: &http.Client{
			Transport:     tr,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, // редиректы отдаём браузеру
		},
	}
}

func (p *Proxy) SetDebug(v bool) { p.debug.Store(v) }

// Заголовки ответа, которые не отдаём клиенту.
var stripResponse = setOf("x-frame-options", "content-security-policy", "content-security-policy-report-only",
	"cross-origin-opener-policy", "cross-origin-embedder-policy", "cross-origin-resource-policy", "permissions-policy",
	"referrer-policy", "strict-transport-security", "set-cookie",
	// транспорт сам распаковал тело: длина и кодировка изменились
	"content-encoding", "content-length", "transfer-encoding", "connection", "keep-alive")

// Заголовки запроса, которые НЕ пересылаем на сайт. Остальное (Authorization, Cookie, X-XSRF-TOKEN, X-Requested-With,
// Content-Type …) пересылается как есть — иначе API сайта отвечает 401.
var dropRequest = setOf("host", "connection", "keep-alive", "transfer-encoding", "te", "trailer", "upgrade",
	"proxy-authorization", "proxy-connection", "content-length", "accept-encoding", "origin", "referer",
	"upgrade-insecure-requests", "forwarded", "x-forwarded-for", "x-forwarded-host", "x-forwarded-proto")

func setOf(items ...string) map[string]bool {
	m := make(map[string]bool, len(items))
	for _, s := range items {
		m[s] = true
	}
	return m
}

// Via — запрос на соседний хост того же сайта: его origin и точка монтирования (/proxy-h/<id>/<scheme>/<host>).
type Via struct{ Origin, Mount string }

// Request — запрос браузера, который нужно выполнить на сайте.
type Request struct {
	Method    string
	ID        string
	Origin    string // origin источника: от него Origin/Referer, по нему определяется «сайт»
	PathQuery string // путь и query на стороне сайта, начинается с "/"
	Header    http.Header
	Body      []byte
	Via       *Via
}

// Response — то, что нужно отдать браузеру. Body нужно закрыть (Close), даже если тела нет.
type Response struct {
	Status  int
	Header  http.Header
	Cookies []string  // уже переписанные Set-Cookie
	Body    io.Reader // потоковое тело (nil, если ответ уже в Data или тела нет)
	Data    []byte    // переписанное HTML/JS целиком
	closer  func()
}

func (r *Response) Close() {
	if r.closer != nil {
		r.closer()
	}
}

// Error — ошибка с кодом ответа для браузера.
type Error struct {
	Code int
	Msg  string
}

func (e *Error) Error() string { return e.Msg }

var idRe = model.IsValidID

// Do выполняет запрос на сайте и подготавливает ответ.
func (p *Proxy) Do(r Request) (*Response, error) {
	if !idRe(r.ID) || !strings.HasPrefix(r.PathQuery, "/") {
		return nil, &Error{400, "bad request"}
	}
	targetOrigin, mount := r.Origin, strings.TrimRight(Prefix, "/")+"/"+r.ID
	if r.Via != nil {
		targetOrigin, mount = r.Via.Origin, r.Via.Mount
	}
	target, err := url.Parse(targetOrigin + r.PathQuery)
	if err != nil || normOrigin(target) != targetOrigin {
		return nil, &Error{400, "bad url"}
	}

	hasBody := r.Method != http.MethodGet && r.Method != http.MethodHead
	var body io.Reader
	if hasBody && len(r.Body) > 0 {
		body = bytes.NewReader(r.Body)
	}
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, r.Method, target.String(), body)
	if err != nil {
		cancel()
		return nil, &Error{400, "bad request"}
	}
	for k, vs := range r.Header {
		lk := strings.ToLower(k)
		if dropRequest[lk] || strings.HasPrefix(lk, "sec-") {
			continue
		}
		req.Header[k] = append([]string(nil), vs...)
	}
	req.Header.Set("Referer", r.Origin+"/")
	// Запрос на соседний хост — «кросс-доменный» для настоящего браузера, он бы прислал Origin страницы.
	if r.Via != nil || hasBody || r.Header.Get("Origin") != "" {
		req.Header.Set("Origin", r.Origin)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		cancel()
		slog.Warn("proxy: сайт недоступен", "method", r.Method, "url", target.Scheme+"://"+target.Host+target.Path, "err", err)
		return nil, &Error{502, "Proxy error: cannot reach " + targetOrigin}
	}
	closeAll := func() { _ = resp.Body.Close(); cancel() }

	// Редиректы внутри того же origin переписываем на точку монтирования, остальные отдаём как есть.
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		if loc := resp.Header.Get("Location"); loc != "" {
			next := loc
			if l, err := target.Parse(loc); err == nil && normOrigin(l) == targetOrigin {
				next = mount + l.EscapedPath() + queryPart(l) + fragPart(l)
			}
			closeAll()
			return &Response{Status: resp.StatusCode, Header: http.Header{"Location": {next}}, closer: func() {}}, nil
		}
	}

	// Ошибки показываем всегда, остальное — в режиме отладки (без query — там токены).
	if p.debug.Load() && resp.StatusCode < 400 {
		slog.Info("proxy", "status", resp.StatusCode, "method", r.Method, "path", target.Path, "source", r.ID)
	}
	if resp.StatusCode >= 400 && !strings.HasSuffix(target.Path, ".map") {
		slog.Warn("proxy: ответ с ошибкой", "status", resp.StatusCode, "method", r.Method, "url", target.Scheme+"://"+target.Host+target.Path, "source", r.ID)
	}

	out := &Response{Status: resp.StatusCode, Header: http.Header{}, closer: closeAll}
	for k, vs := range resp.Header {
		if stripResponse[strings.ToLower(k)] {
			continue
		}
		out.Header[k] = append([]string(nil), vs...)
	}
	for _, c := range resp.Header.Values("Set-Cookie") {
		out.Cookies = append(out.Cookies, rewriteCookie(c, mount))
	}

	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	isHTML := strings.Contains(ct, "text/html")
	isCSS := strings.Contains(ct, "text/css")
	isJS := (strings.Contains(ct, "javascript") || strings.Contains(ct, "ecmascript")) && !strings.HasSuffix(target.Path, ".map")
	if r.Method == http.MethodHead || resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusNotModified ||
		!(isHTML || isJS || isCSS) || strings.Contains(ct, "utf-16") {
		out.Body = resp.Body
		return out, nil
	}

	// HTML/JS читаем целиком (в пределах лимита), остальное — потоком.
	buf, err := io.ReadAll(io.LimitReader(resp.Body, maxRewriteBytes+1))
	if err != nil {
		closeAll()
		slog.Warn("proxy: ошибка чтения ответа", "url", target.Host+target.Path, "err", err)
		return nil, &Error{502, "Proxy error: cannot read response from " + targetOrigin}
	}
	if len(buf) > maxRewriteBytes {
		out.Body = io.MultiReader(bytes.NewReader(buf), resp.Body) // слишком большой — без переписывания
		return out, nil
	}
	neutralized := 0
	if isHTML {
		var st Stats
		buf, st = RewriteHTML(buf, injection(r.ID, r.Origin), mount)
		neutralized = st.Neutralized
		if st.CSPRemoved > 0 {
			slog.Info("proxy: удалён meta Content-Security-Policy", "n", st.CSPRemoved, "path", target.Path, "source", r.ID)
		}
		out.Header.Set("Cache-Control", "no-store")
	} else if isCSS {
		buf = RewriteCSS(buf, mount)
	} else {
		var s string
		s, neutralized = rewrite.NeutralizeFrameChecks(string(buf))
		if neutralized > 0 {
			buf = []byte(s)
		}
	}
	if neutralized > 0 {
		slog.Info("proxy: скрыта проверка «страница во фрейме»", "n", neutralized, "path", target.Path, "source", r.ID)
	}
	out.Data = buf
	_ = resp.Body.Close()
	cancel()
	out.closer = func() {}
	return out, nil
}

func queryPart(u *url.URL) string {
	if u.RawQuery != "" || u.ForceQuery {
		return "?" + u.RawQuery
	}
	return ""
}

func fragPart(u *url.URL) string {
	if u.Fragment != "" {
		return "#" + u.EscapedFragment()
	}
	return ""
}

// errCode превращает ошибку Do в код ответа и текст.
func errCode(err error) (int, string) {
	if e, ok := errors.AsType[*Error](err); ok {
		return e.Code, e.Msg
	}
	return 502, "proxy error"
}
