package localnet

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTarget(t *testing.T) {
	for in, want := range map[string]string{"127.0.0.1:24050": "127.0.0.1:24050", "localhost:1": "127.0.0.1:1", "LOCALHOST:65535": "127.0.0.1:65535", "[::1]:80": "[::1]:80"} {
		if got, err := Target(in); err != nil || got != want {
			t.Errorf("%s: %q %v", in, got, err)
		}
	}
	for _, in := range []string{"", "example.com:80", "192.168.1.5:80", "0.0.0.0:80", "127.0.0.1", "127.0.0.1:0", "127.0.0.1:70000", "127.0.0.1:x", "user@127.0.0.1:80", "127.0.0.1.evil.com:80", "10.0.0.1:80"} {
		if _, err := Target(in); err == nil {
			t.Errorf("%q must be rejected", in)
		}
	}
}

func TestGet(t *testing.T) {
	var gotOrigin, gotReferer, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotOrigin, gotReferer, gotPath = r.Header.Get("Origin"), r.Header.Get("Referer"), r.URL.RequestURI()
		switch r.URL.Path {
		case "/json/v2":
			w.Write([]byte(`{"ok":1}`))
		case "/redir":
			http.Redirect(w, r, "http://example.com/", 302)
		case "/big":
			w.Write([]byte(strings.Repeat("x", MaxBody+10)))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")

	r, err := Get(context.Background(), addr, "/json/v2?a=1")
	if err != nil || r.Status != 200 || r.Body != `{"ok":1}` {
		t.Fatalf("%+v %v", r, err)
	}
	if gotOrigin != "" || gotReferer != "" || gotPath != "/json/v2?a=1" {
		t.Errorf("origin=%q referer=%q path=%q", gotOrigin, gotReferer, gotPath)
	}
	if r, err := Get(context.Background(), addr, "/nope"); err != nil || r.Status != 404 {
		t.Errorf("404: %+v %v", r, err)
	}
	if r, err := Get(context.Background(), addr, "/redir"); err != nil || r.Status != 302 {
		t.Errorf("redirect must not be followed: %+v %v", r, err)
	}
	if _, err := Get(context.Background(), addr, "/big"); err != ErrBig {
		t.Errorf("big: %v", err)
	}
	for _, p := range []string{"", "x", "//evil.com/", "/a b", "/a\\b", "/" + strings.Repeat("a", 600)} {
		if _, err := Get(context.Background(), addr, p); err != ErrPath {
			t.Errorf("path %q: %v", p, err)
		}
	}
	if _, err := Get(context.Background(), "example.com:80", "/"); err != ErrTarget {
		t.Errorf("remote target: %v", err)
	}
	if _, err := Get(context.Background(), "127.0.0.1:1", "/"); err == nil {
		t.Error("closed port must fail")
	}
}
