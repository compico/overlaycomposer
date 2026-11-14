package ocplugin

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

type op struct {
	Op  string
	Arg map[string]any
}

// fakeHost запоминает операции и отвечает как хост: storage в памяти, ошибки по ключу "deny".
type fakeHost struct {
	ops     []op
	storage map[string]json.RawMessage
}

func useFakeHost(t *testing.T) *fakeHost {
	t.Helper()
	current = nil
	f := &fakeHost{storage: map[string]json.RawMessage{}}
	SetHost(func(o string, arg []byte) []byte {
		var a map[string]any
		_ = json.Unmarshal(arg, &a)
		f.ops = append(f.ops, op{o, a})
		switch o {
		case "storage.get":
			v, ok := f.storage[a["key"].(string)]
			if !ok {
				return []byte(`{"found":false}`)
			}
			return []byte(`{"found":true,"value":` + string(v) + `}`)
		case "storage.set":
			if a["key"] == "deny" {
				return []byte(`{"error":"permission denied: storage"}`)
			}
			v, _ := json.Marshal(a["value"])
			f.storage[a["key"].(string)] = v
		case "storage.delete":
			delete(f.storage, a["key"].(string))
		case "log":
			return nil
		}
		return []byte(`{}`)
	})
	t.Cleanup(func() { SetHost(nil); current = nil })
	return f
}

func (f *fakeHost) last(name string) op {
	for i := len(f.ops) - 1; i >= 0; i-- {
		if f.ops[i].Op == name {
			return f.ops[i]
		}
	}
	return op{}
}

func req(method, path, query, body string) []byte {
	q := map[string]string{}
	if query != "" {
		for _, kv := range strings.Split(query, "&") {
			p := strings.SplitN(kv, "=", 2)
			q[p[0]] = p[1]
		}
	}
	b := json.RawMessage(`null`)
	if body != "" {
		b = json.RawMessage(body)
	}
	out, _ := json.Marshal(map[string]any{"method": method, "path": path, "query": q, "body": b})
	return out
}

func decode(t *testing.T, raw []byte) (int, string) {
	t.Helper()
	var r struct {
		Status int             `json:"status"`
		Body   json.RawMessage `json:"body"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatalf("response %q: %v", raw, err)
	}
	return r.Status, string(r.Body)
}

func TestMetaJSONCollectsRoutesAndSubscriptions(t *testing.T) {
	useFakeHost(t)
	p := New(Meta{ID: "demo", Name: "Demo", Version: "1.2.3", Permissions: []string{PermStorage}})
	p.GET("/a", func(*Ctx) error { return nil })
	p.POST("/a/b", func(*Ctx) error { return nil })
	p.OnRedemption(func(Redemption) {})
	var m struct {
		ID, Name, Version string
		APIVersion        int
		Permissions       []string
		Routes            []route
		Subscriptions     []string
	}
	if err := json.Unmarshal(p.metaJSON(), &m); err != nil {
		t.Fatal(err)
	}
	if m.ID != "demo" || m.APIVersion != 1 || len(m.Routes) != 2 || m.Routes[1].Method != "POST" || m.Routes[1].Path != "/a/b" ||
		m.Routes[0].Handler == m.Routes[1].Handler || len(m.Subscriptions) != 1 || m.Subscriptions[0] != "twitch.redemption" {
		t.Fatalf("meta: %+v", m)
	}
	// без OnRedemption подписки нет
	current = nil
	q := New(Meta{ID: "x", Name: "x", Version: "1.0.0"})
	if strings.Contains(string(q.metaJSON()), "subscriptions") {
		t.Fatal("no subscription without OnRedemption")
	}
}

func TestRoutes(t *testing.T) {
	useFakeHost(t)
	p := New(Meta{ID: "demo", Name: "Demo", Version: "1.0.0"})
	var bound struct{ N int }
	p.GET("/hello", func(c *Ctx) error { return c.JSON(201, map[string]string{"hi": c.Query("name"), "m": c.Method}) })
	p.POST("/bind", func(c *Ctx) error {
		if err := c.Bind(&bound); err != nil {
			return c.Error(400, "bad body")
		}
		return c.JSON(200, bound)
	})
	p.GET("/none", func(c *Ctx) error { return c.NoContent() })
	p.GET("/silent", func(*Ctx) error { return nil })
	p.GET("/fail", func(*Ctx) error { return errTest })
	p.GET("/panic", func(*Ctx) error { var m map[string]int; m["x"] = 1; return nil })
	name := func(method, path string) string {
		for _, r := range p.routes {
			if r.Method == method && r.Path == path {
				return r.Handler
			}
		}
		t.Fatalf("no route %s %s", method, path)
		return ""
	}
	if st, b := decode(t, p.dispatch(name("GET", "/hello"), req("GET", "/hello", "name=oc", ""))); st != 201 || b != `{"hi":"oc","m":"GET"}` {
		t.Fatalf("hello: %d %s", st, b)
	}
	if st, b := decode(t, p.dispatch(name("POST", "/bind"), req("POST", "/bind", "", `{"N":7}`))); st != 200 || b != `{"N":7}` || bound.N != 7 {
		t.Fatalf("bind: %d %s", st, b)
	}
	if st, b := decode(t, p.dispatch(name("GET", "/none"), req("GET", "/none", "", ""))); st != 204 || b != "" {
		t.Fatalf("no content: %d %s", st, b)
	}
	if st, _ := decode(t, p.dispatch(name("GET", "/silent"), req("GET", "/silent", "", ""))); st != 204 {
		t.Fatalf("a handler that answers nothing is 204, got %d", st)
	}
	// sad path: пустое тело, битое тело, ошибка обработчика, паника, неизвестный обработчик, битый запрос
	if st, b := decode(t, p.dispatch(name("POST", "/bind"), req("POST", "/bind", "", ""))); st != 400 || !strings.Contains(b, "bad body") {
		t.Fatalf("empty body: %d %s", st, b)
	}
	if st, _ := decode(t, p.dispatch(name("POST", "/bind"), req("POST", "/bind", "", `{"N":"x"}`))); st != 400 {
		t.Fatalf("bad body: %d", st)
	}
	if st, b := decode(t, p.dispatch(name("GET", "/fail"), req("GET", "/fail", "", ""))); st != 500 || strings.Contains(b, "boom") {
		t.Fatalf("handler error must be a generic 500: %d %s", st, b)
	}
	if st, b := decode(t, p.dispatch(name("GET", "/panic"), req("GET", "/panic", "", ""))); st != 500 || strings.Contains(b, "nil map") {
		t.Fatalf("panic must be a generic 500: %d %s", st, b)
	}
	if st, _ := decode(t, p.dispatch(name("GET", "/hello"), req("GET", "/hello", "name=again", ""))); st != 201 {
		t.Fatal("the plugin must keep working after a recovered panic")
	}
	if st, _ := decode(t, p.dispatch("nope", req("GET", "/", "", ""))); st != 404 {
		t.Fatal("unknown handler")
	}
	if st, _ := decode(t, p.dispatch(name("GET", "/hello"), []byte("{broken"))); st != 400 {
		t.Fatal("broken request")
	}
}

type testErr string

func (e testErr) Error() string { return string(e) }

var errTest = testErr("boom: secret details")

func TestEvents(t *testing.T) {
	f := useFakeHost(t)
	p := New(Meta{ID: "demo", Name: "Demo", Version: "1.0.0"})
	var red Redemption
	var ticks int
	var resp HTTPResponse
	p.OnRedemption(func(r Redemption) { red = r })
	p.OnTimer("t", func() { ticks++ })
	p.OnTimer("bad", func() { panic("timer bug") })
	p.OnHTTPResponse("f1", func(r HTTPResponse) { resp = r })
	ev := func(typ, data string) { p.dispatch("on_event", []byte(`{"type":"`+typ+`","data":`+data+`}`)) }

	ev("twitch.redemption", `{"id":"r1","rewardId":"rw","userLogin":"viewer","input":"https://x/y"}`)
	if red.ID != "r1" || red.RewardID != "rw" || red.UserLogin != "viewer" || red.Input != "https://x/y" {
		t.Fatalf("redemption: %+v", red)
	}
	ev("timer", `{"id":"t"}`)
	ev("timer", `{"id":"t"}`)
	ev("timer", `{"id":"unknown"}`) // нет обработчика — молча игнорируем
	ev("timer", `{"id":"bad"}`)     // паника в обработчике не должна уронить остальное
	ev("timer", `{"id":"t"}`)
	if ticks != 3 {
		t.Fatalf("ticks = %d", ticks)
	}
	if f.last("log").Arg["level"] != "error" {
		t.Fatalf("a recovered panic must be logged as error: %+v", f.last("log"))
	}
	ev("http.response", `{"id":"f1","status":200,"body":"ok"}`)
	if resp.Status != 200 || resp.Body != "ok" {
		t.Fatalf("response: %+v", resp)
	}
	ev("http.response", `{"id":"other","status":500}`)
	ev("twitch.redemption", `not json`)
	ev("something.else", `{}`)
	p.dispatch("on_event", []byte(`{broken`))
}

func TestHostOps(t *testing.T) {
	f := useFakeHost(t)
	type item struct {
		A int
		B string
	}
	if _, found, err := Load[item]("k"); err != nil || found {
		t.Fatalf("missing key: %v %v", found, err)
	}
	if err := Store("k", item{1, "x"}); err != nil {
		t.Fatal(err)
	}
	v, found, err := Load[item]("k")
	if err != nil || !found || v.A != 1 || v.B != "x" {
		t.Fatalf("round trip: %+v %v %v", v, found, err)
	}
	if err := Remove("k"); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := Load[item]("k"); found {
		t.Fatal("removed")
	}
	// sad path: ошибка хоста приходит как error; значение, которое нельзя закодировать
	if err := Store("deny", 1); err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("host error must surface: %v", err)
	}
	if err := Store("k", make(chan int)); err == nil {
		t.Fatal("unencodable value")
	}
	f.storage["wrong"] = json.RawMessage(`"string"`)
	if _, _, err := Load[item]("wrong"); err == nil {
		t.Fatal("type mismatch must be an error")
	}

	if err := SetTimer("t", 1500*time.Millisecond, true); err != nil {
		t.Fatal(err)
	}
	if a := f.last("timer.set").Arg; a["id"] != "t" || a["ms"] != float64(1500) || a["repeat"] != true {
		t.Fatalf("timer.set: %v", a)
	}
	ClearTimer("t")
	if f.last("timer.clear").Arg["id"] != "t" {
		t.Fatal("timer.clear")
	}
	if err := Fetch("f", FetchRequest{URL: "https://example.com", Method: "POST", Body: "b"}); err != nil {
		t.Fatal(err)
	}
	if a := f.last("http.fetch").Arg; a["id"] != "f" || a["url"] != "https://example.com" || a["method"] != "POST" || a["body"] != "b" {
		t.Fatalf("http.fetch: %v", a)
	}
	_ = FulfillRedemption("rw", "r1")
	if a := f.last("twitch.redemption.update").Arg; a["status"] != "FULFILLED" || a["rewardId"] != "rw" || a["id"] != "r1" {
		t.Fatalf("fulfill: %v", a)
	}
	_ = CancelRedemption("rw", "r2")
	if f.last("twitch.redemption.update").Arg["status"] != "CANCELED" {
		t.Fatal("cancel")
	}
	Info("hello", "n", 1, "dangling")
	if l := f.last("log").Arg; l["level"] != "info" || l["msg"] != "hello" || l["fields"].(map[string]any)["n"] != float64(1) {
		t.Fatalf("log: %v", l)
	}
}

func TestNoHost(t *testing.T) {
	current = nil
	SetHost(nil)
	if err := Store("k", 1); err == nil || !strings.Contains(err.Error(), "host is not available") {
		t.Fatalf("outside the host the operations must fail clearly: %v", err)
	}
	Info("must not panic without a host")
}

func TestConfigAndInit(t *testing.T) {
	useFakeHost(t)
	p := New(Meta{ID: "demo", Name: "Demo", Version: "1.0.0"})
	var got struct{ Token string }
	inits := 0
	p.OnInit(func() error { inits++; return p.Config(&got) })
	p.init([]byte(`{"apiVersion":1,"config":{"Token":"abc"}}`))
	if inits != 1 || got.Token != "abc" {
		t.Fatalf("config: %d %+v", inits, got)
	}
	p.init([]byte(`{"apiVersion":1}`)) // без config остаётся прежний
	// sad path: ошибка OnInit — паника (trap), хост пометит плагин как failed
	p.OnInit(func() error { return errTest })
	defer func() {
		if r := recover(); r == nil || !strings.Contains(r.(string), "OnInit") {
			t.Fatalf("OnInit error must trap: %v", r)
		}
	}()
	p.init([]byte(`{}`))
}

func TestRegistrationMistakesPanic(t *testing.T) {
	useFakeHost(t)
	p := New(Meta{ID: "demo", Name: "Demo", Version: "1.0.0"})
	p.GET("/a", func(*Ctx) error { return nil })
	mustPanic := func(what string, f func()) {
		t.Helper()
		defer func() {
			if recover() == nil {
				t.Errorf("%s must panic", what)
			}
		}()
		f()
	}
	mustPanic("duplicate route", func() { p.GET("/a", func(*Ctx) error { return nil }) })
	mustPanic("path without slash", func() { p.GET("b", func(*Ctx) error { return nil }) })
	mustPanic("nil handler", func() { p.GET("/c", nil) })
	mustPanic("second New", func() { New(Meta{}) })
	p.POST("/a", func(*Ctx) error { return nil }) // другой метод — можно
}

func TestMetaWidgetsAreReported(t *testing.T) {
	current = nil
	p := New(Meta{ID: "w", Name: "W", Version: "1.0.0", Widgets: []Widget{{ID: "main", Name: "Main", Entry: "index.html", Width: 320}}})
	var m struct {
		Widgets []Widget `json:"widgets"`
	}
	if err := json.Unmarshal(p.metaJSON(), &m); err != nil {
		t.Fatal(err)
	}
	if len(m.Widgets) != 1 || m.Widgets[0].Entry != "index.html" || m.Widgets[0].Width != 320 {
		t.Fatalf("widgets: %+v", m.Widgets)
	}
	current = nil
	if strings.Contains(string(New(Meta{ID: "x", Name: "X", Version: "1.0.0"}).metaJSON()), "widgets") {
		t.Fatal("no widgets: the field must be omitted")
	}
}
