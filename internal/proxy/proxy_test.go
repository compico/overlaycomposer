package proxy

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/compico/overlaycomposer/internal/model"
)

func TestSiteOf(t *testing.T) {
	cases := map[string]string{
		"www.example.com": "example.com", "example.com": "example.com", "a.b.example.com": "example.com",
		"www.example.co.uk": "example.co.uk", "localhost": "localhost", "127.0.0.1": "127.0.0.1", "::1": "::1",
		"centrifugo.donationalerts.com": "donationalerts.com",
	}
	for in, want := range cases {
		if got := SiteOf(in); got != want {
			t.Errorf("SiteOf(%q)=%q want %q", in, got, want)
		}
	}
}

func TestSiblingOrigin(t *testing.T) {
	o := "https://www.example.com"
	if got := SiblingOrigin(o, "https", "socket8.example.com"); got != "https://socket8.example.com" {
		t.Errorf("sibling: %q", got)
	}
	for _, host := range []string{"evil.com", "example.com.evil.com", "user@example.com", "socket.example.com/x", "socket.example.com?x=1", "x.example.com#a", ""} {
		if got := SiblingOrigin(o, "https", host); got != "" {
			t.Errorf("host %q must be rejected, got %q", host, got)
		}
	}
	if SiblingOrigin(o, "ftp", "a.example.com") != "" {
		t.Error("scheme")
	}
}

func TestUpstreamOrigin(t *testing.T) {
	mk := func(u string, proxy bool) *model.Source {
		return &model.Source{ID: "x", Data: model.SourceData{URL: u, Proxy: proxy}}
	}
	if got := UpstreamOrigin(mk("https://Example.com:443/a/b?c", true)); got != "https://example.com" {
		t.Errorf("origin: %q", got)
	}
	if UpstreamOrigin(mk("https://example.com", false)) != "" || UpstreamOrigin(mk("/widgets/x", true)) != "" || UpstreamOrigin(nil) != "" {
		t.Error("must be empty")
	}
}

func TestRefererProxyID(t *testing.T) {
	if got := RefererProxyID("http://localhost:8000/proxy/chat/index.html?x", "localhost:8000"); got != "chat" {
		t.Errorf("got %q", got)
	}
	for _, ref := range []string{"http://other:1/proxy/chat/", "http://localhost:8000/overlay", "", "http://localhost:8000/proxy/chat"} {
		if RefererProxyID(ref, "localhost:8000") != "" {
			t.Errorf("must be empty for %q", ref)
		}
	}
}

func TestRewriteCookie(t *testing.T) {
	got := rewriteCookie("sid=abc; Domain=.example.com; Path=/; Secure; HttpOnly; SameSite=None; Max-Age=60", "/proxy/chat")
	want := "sid=abc; Path=/proxy/chat/; SameSite=Lax; HttpOnly; Max-Age=60"
	if got != want {
		t.Errorf("\n got %q\nwant %q", got, want)
	}
}

func TestRewriteHTML(t *testing.T) {
	const inj = "<!--INJ-->"
	t.Run("после charset-meta внутри head", func(t *testing.T) {
		out, _ := RewriteHTML([]byte(`<!doctype html><html><head><meta charset="utf-8"><title>x</title></head><body></body></html>`), inj, "")
		want := `<!doctype html><html><head><meta charset="utf-8"><!--INJ--><title>x</title></head><body></body></html>`
		if string(out) != want {
			t.Errorf("\n got %s\nwant %s", out, want)
		}
	})
	t.Run("<head> внутри комментария не считается", func(t *testing.T) {
		out, _ := RewriteHTML([]byte(`<!-- <head> --><html><head><title>x</title></head></html>`), inj, "")
		want := `<!-- <head> --><html><head><!--INJ--><title>x</title></head></html>`
		if string(out) != want {
			t.Errorf("got %s", out)
		}
	})
	t.Run("без head", func(t *testing.T) {
		out, _ := RewriteHTML([]byte(`<html><body>hi</body></html>`), inj, "")
		if string(out) != `<html><!--INJ--><body>hi</body></html>` {
			t.Errorf("got %s", out)
		}
	})
	t.Run("фрагмент и пустой текст", func(t *testing.T) {
		if out, _ := RewriteHTML([]byte(`<div>x</div>`), inj, ""); string(out) != `<!--INJ--><div>x</div>` {
			t.Errorf("fragment: %s", out)
		}
		if out, _ := RewriteHTML([]byte(`plain`), inj, ""); string(out) != `<!--INJ-->plain` {
			t.Errorf("text: %s", out)
		}
	})
	t.Run("скрипты и обработчики", func(t *testing.T) {
		src := `<html><head></head><body onload="if(window.self!==window.top){x()}">` +
			`<script>if (window.self !== window.top) { stop() }</script>` +
			`<script type="application/json">{"a":"window.self !== window.top"}</script>` +
			`<p>window.self !== window.top</p></body></html>`
		out, st := RewriteHTML([]byte(src), inj, "")
		s := string(out)
		if st.Neutralized != 2 {
			t.Errorf("neutralized=%d\n%s", st.Neutralized, s)
		}
		if !strings.Contains(s, `if((false)){x()}`) || !strings.Contains(s, `if ((false)) { stop() }`) {
			t.Errorf("scripts not rewritten: %s", s)
		}
		if !strings.Contains(s, `{"a":"window.self !== window.top"}`) || !strings.Contains(s, `<p>window.self !== window.top</p>`) {
			t.Errorf("json/text must stay: %s", s)
		}
	})
	t.Run("meta CSP удаляется", func(t *testing.T) {
		out, st := RewriteHTML([]byte(`<head><meta http-equiv="Content-Security-Policy" content="script-src 'none'"><meta name="a" content="b"></head>`), inj, "")
		if st.CSPRemoved != 1 || strings.Contains(string(out), "script-src") || !strings.Contains(string(out), `name="a"`) {
			t.Errorf("got %s", out)
		}
	})
	t.Run("без изменений документ байт в байт", func(t *testing.T) {
		src := "<!DOCTYPE html>\n<HTML lang=en>\n<head>\n  <meta charset=utf-8>\n</head><body class='a  b'><br/><img src=x alt=\"&amp;\">&nbsp;<!-- c --></body></HTML>"
		out, _ := RewriteHTML([]byte(src), "", "")
		if string(out) != src {
			t.Errorf("\n got %q\nwant %q", out, src)
		}
	})
}

func TestInjectionJSON(t *testing.T) {
	s := injection("chat", "https://www.example.com")
	for _, want := range []string{`"id":"chat"`, `"site":"example.com"`, `"wsScheme":"wss"`, `"host":"www.example.com"`, `name="referrer"`} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s in %s", want, s)
		}
	}
}

// ---- Do на настоящем HTTP-сервере ----

func newUpstream(t *testing.T) (*httptest.Server, *http.Header) {
	t.Helper()
	var seen http.Header
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
		http.SetCookie(w, &http.Cookie{Name: "sid", Value: "1", Path: "/", Domain: "example.com", Secure: true, HttpOnly: true})
		io.WriteString(w, `<html><head><title>t</title></head><body><script>if(window.self!==window.top){return}</script></body></html>`)
	})
	mux.HandleFunc("/app.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		w.Header().Set("Content-Encoding", "gzip")
		gz := gzip.NewWriter(w)
		io.WriteString(gz, `var a = window.self !== window.top;`)
		gz.Close()
	})
	mux.HandleFunc("/old", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/new?x=1", http.StatusFound)
	})
	mux.HandleFunc("/away", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://other.example.org/x", http.StatusFound)
	})
	mux.HandleFunc("/api", func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		b, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Write(append([]byte(r.Method+":"), b...))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &seen
}

func readAll(t *testing.T, r *Response) string {
	t.Helper()
	defer r.Close()
	if r.Data != nil {
		return string(r.Data)
	}
	if r.Body == nil {
		return ""
	}
	b, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestDoHTML(t *testing.T) {
	srv, seen := newUpstream(t)
	p := New(nil)
	hdr := http.Header{"Authorization": {"Bearer x"}, "Cookie": {"a=b"}, "Referer": {"http://localhost:8000/proxy/w/"}, "Sec-Fetch-Mode": {"navigate"}, "Accept-Encoding": {"br"}, "X-Forwarded-For": {"1.2.3.4"}}
	resp, err := p.Do(Request{Method: "GET", ID: "w", Origin: srv.URL, PathQuery: "/", Header: hdr})
	if err != nil {
		t.Fatal(err)
	}
	body := readAll(t, resp)
	if !strings.Contains(body, "<title>t</title>") || !strings.Contains(body, `name="referrer"`) || !strings.Contains(body, "(false)") {
		t.Errorf("body: %s", body)
	}
	if resp.Header.Get("X-Frame-Options") != "" || resp.Header.Get("Content-Security-Policy") != "" {
		t.Errorf("security headers leaked: %v", resp.Header)
	}
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Error("no-store")
	}
	if len(resp.Cookies) != 1 || resp.Cookies[0] != "sid=1; Path=/proxy/w/; SameSite=Lax; HttpOnly" {
		t.Errorf("cookies: %q", resp.Cookies)
	}
	got := *seen
	if got.Get("Authorization") != "Bearer x" || got.Get("Cookie") != "a=b" {
		t.Errorf("auth/cookie must pass: %v", got)
	}
	if got.Get("Referer") != srv.URL+"/" || got.Get("Sec-Fetch-Mode") != "" || got.Get("X-Forwarded-For") != "" || got.Get("Origin") != "" {
		t.Errorf("headers: %v", got)
	}
}

func TestDoJSGzip(t *testing.T) {
	srv, _ := newUpstream(t)
	resp, err := New(nil).Do(Request{Method: "GET", ID: "w", Origin: srv.URL, PathQuery: "/app.js", Header: http.Header{}})
	if err != nil {
		t.Fatal(err)
	}
	if got := readAll(t, resp); got != `var a = (false);` {
		t.Errorf("js: %q", got)
	}
	if resp.Header.Get("Content-Encoding") != "" {
		t.Error("content-encoding must be removed")
	}
}

func TestDoRedirects(t *testing.T) {
	srv, _ := newUpstream(t)
	p := New(nil)
	resp, err := p.Do(Request{Method: "GET", ID: "w", Origin: srv.URL, PathQuery: "/old", Header: http.Header{}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != 302 || resp.Header.Get("Location") != "/proxy/w/new?x=1" {
		t.Errorf("same-origin redirect: %d %v", resp.Status, resp.Header)
	}
	resp, _ = p.Do(Request{Method: "GET", ID: "w", Origin: srv.URL, PathQuery: "/away", Header: http.Header{}})
	if resp.Header.Get("Location") != "https://other.example.org/x" {
		t.Errorf("foreign redirect: %v", resp.Header)
	}
}

func TestDoPostAndVia(t *testing.T) {
	srv, seen := newUpstream(t)
	p := New(nil)
	resp, err := p.Do(Request{Method: "POST", ID: "w", Origin: "https://www.example.com", PathQuery: "/api", Header: http.Header{"Content-Type": {"application/json"}}, Body: []byte(`{"a":1}`),
		Via: &Via{Origin: srv.URL, Mount: "/proxy-h/w/http/x"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := readAll(t, resp); got != `POST:{"a":1}` {
		t.Errorf("body: %q", got)
	}
	if (*seen).Get("Origin") != "https://www.example.com" || (*seen).Get("Content-Type") != "application/json" {
		t.Errorf("headers: %v", *seen)
	}
}

func TestDoRejects(t *testing.T) {
	p := New(nil)
	for _, r := range []Request{
		{Method: "GET", ID: "../x", Origin: "http://a.com", PathQuery: "/"},
		{Method: "GET", ID: "w", Origin: "http://a.com", PathQuery: "x"},
		{Method: "GET", ID: "w", Origin: "http://a.com", PathQuery: "/@evil.com/"},
	} {
		if _, err := p.Do(r); err == nil {
			t.Errorf("must fail: %+v", r)
		}
	}
	if _, err := p.Do(Request{Method: "GET", ID: "w", Origin: "http://127.0.0.1:1", PathQuery: "/", Header: http.Header{}}); err == nil {
		t.Error("unreachable must fail")
	} else if code, _ := errCode(err); code != 502 {
		t.Errorf("code %d", code)
	}
}

func TestRefererChain(t *testing.T) {
	p := New(nil)
	host := "localhost:8000"
	// прямой Referer
	if p.refererID("http://localhost:8000/proxy/w/overlay/", host) != "w" {
		t.Error("direct")
	}
	// путь ещё не отдавался
	if p.refererID("http://localhost:8000/overlay/assets/app.js", host) != "" {
		t.Error("unknown path must not resolve")
	}
	// после отдачи /overlay/assets/app.js для источника w его зависимости находят источник по Referer = URL модуля
	p.served.set("/overlay/assets/app.js", "w")
	if p.refererID("http://localhost:8000/overlay/assets/app.js", host) != "w" {
		t.Error("chain")
	}
	if p.refererID("http://other:1/overlay/assets/app.js", host) != "" {
		t.Error("foreign host")
	}
	p.served.set("/proxy/x/y", "z") // пути прокси не запоминаем
	if p.refererID("http://localhost:8000/proxy/x/y", host) != "x" {
		t.Error("proxy path stays direct")
	}
}

func TestRemountHTMLAndCSS(t *testing.T) {
	src := `<head><link rel="stylesheet" href="/overlay/assets/a.css"><script type="module" src="/overlay/assets/a.js"></script>` +
		`<link href="//cdn.x/y.css" rel=stylesheet><img src="rel.png" srcset="/a.png 1x, /b.png 2x"><a href="/proxy/s/x"></a></head>`
	out, st := RewriteHTML([]byte(src), "", "/proxy/s")
	got := string(out)
	for _, want := range []string{
		`href="/proxy/s/overlay/assets/a.css"`, `src="/proxy/s/overlay/assets/a.js"`, `href="//cdn.x/y.css"`,
		`src="rel.png"`, `srcset="/proxy/s/a.png 1x, /proxy/s/b.png 2x"`, `href="/proxy/s/x"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("нет %s в %s", want, got)
		}
	}
	if st.Remounted != 3 {
		t.Errorf("Remounted=%d, want 3", st.Remounted)
	}

	css := `@import "/a.css"; .x{background:url(/img/a.png)} .y{src:url( '/f.woff' ),url(data:x),url(//h/x),url(rel.png)}`
	if got := string(RewriteCSS([]byte(css), "/proxy/s")); got !=
		`@import "/proxy/s/a.css"; .x{background:url(/proxy/s/img/a.png)} .y{src:url('/proxy/s/f.woff'),url(data:x),url(//h/x),url(rel.png)}` {
		t.Errorf("css: %s", got)
	}
}

func TestInjectionBlocklist(t *testing.T) {
	if inj := injection("s", "https://example.com"); !strings.Contains(inj, `"block":["mytopf.com"`) {
		t.Errorf("нет block: %s", inj)
	}
}

func TestDoCSS(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/css/base.css", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css")
		w.Header().Set("Content-Encoding", "gzip")
		gz := gzip.NewWriter(w)
		io.WriteString(gz, "html {\n  width: 100%;\n  height: 100%;\n}\n.a{background:url(/img/a.png)}")
		gz.Close()
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	resp, err := New(nil).Do(Request{Method: "GET", ID: "w", Origin: srv.URL, PathQuery: "/css/base.css", Header: http.Header{"Accept-Encoding": {"gzip"}}})
	if err != nil {
		t.Fatal(err)
	}
	h := resp.Header.Get("Content-Type")
	got := readAll(t, resp)
	if h != "text/css" || !strings.Contains(got, "height: 100%;") || !strings.Contains(got, "url(/proxy/w/img/a.png)") {
		t.Errorf("ct=%q body=%q hdr=%v", h, got, resp.Header)
	}
}
