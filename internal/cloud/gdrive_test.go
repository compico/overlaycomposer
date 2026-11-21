package cloud

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type memTokens struct{ v string }

func (m *memTokens) Load() (string, error) { return m.v, nil }
func (m *memTokens) Save(t string) error   { m.v = t; return nil }

type fakeFile struct {
	id, name string
	isDir    bool
	parent   string
	data     []byte
	props    map[string]string
}

// fakeGoogle — минимум Google: токены и Drive API (appDataFolder).
type fakeGoogle struct {
	mu        sync.Mutex
	files     []*fakeFile // включая папки (isDir)
	next      int
	refreshes int
	revoked   bool // refresh отвечает invalid_grant
	staleOnce bool // первый запрос к API получает 401
	lastForm  url.Values
	srv       *httptest.Server
}

func newFakeGoogle(t *testing.T) *fakeGoogle {
	g := &fakeGoogle{}
	mux := http.NewServeMux()
	mux.HandleFunc("/token", g.token)
	mux.HandleFunc("/drive/v3/files", g.list)
	mux.HandleFunc("/drive/v3/files/", g.item)
	mux.HandleFunc("/upload/drive/v3/files", g.upload)
	mux.HandleFunc("/upload/drive/v3/files/", g.upload)
	g.srv = httptest.NewServer(mux)
	t.Cleanup(g.srv.Close)
	return g
}

func (g *fakeGoogle) cfg() GoogleConfig {
	return GoogleConfig{ClientID: "cid", ClientSecret: "sec", AuthURL: g.srv.URL + "/auth", TokenURL: g.srv.URL + "/token", APIURL: g.srv.URL + "/drive/v3", UploadURL: g.srv.URL + "/upload/drive/v3"}
}

func (g *fakeGoogle) token(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	g.mu.Lock()
	defer g.mu.Unlock()
	g.lastForm = r.PostForm
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		if r.PostForm.Get("code") != "good-code" || r.PostForm.Get("code_verifier") == "" {
			w.WriteHeader(400)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "acc-0", "refresh_token": "ref-1", "expires_in": 3600})
	case "refresh_token":
		if g.revoked || r.PostForm.Get("refresh_token") != "ref-1" {
			w.WriteHeader(400)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
			return
		}
		g.refreshes++
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "acc-" + strconv.Itoa(g.refreshes), "expires_in": 3600})
	}
}

func (g *fakeGoogle) authed(w http.ResponseWriter, r *http.Request) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.staleOnce {
		g.staleOnce = false
		w.WriteHeader(401)
		return false
	}
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer acc-") {
		w.WriteHeader(401)
		return false
	}
	return true
}

func (f *fakeFile) json() map[string]any {
	return map[string]any{"id": f.id, "name": f.name, "mimeType": map[bool]string{true: folderMime, false: "application/json"}[f.isDir], "size": strconv.Itoa(len(f.data)), "modifiedTime": time.Now().UTC().Format(time.RFC3339), "appProperties": f.props}
}

func (g *fakeGoogle) list(w http.ResponseWriter, r *http.Request) {
	if !g.authed(w, r) {
		return
	}
	if r.Method == http.MethodPost { // создание папки (JSON без содержимого)
		var meta struct{ Name, MimeType string }
		_ = json.NewDecoder(r.Body).Decode(&meta)
		g.mu.Lock()
		defer g.mu.Unlock()
		g.next++
		f := &fakeFile{id: "dir" + strconv.Itoa(g.next), name: meta.Name, isDir: meta.MimeType == folderMime}
		g.files = append(g.files, f)
		_ = json.NewEncoder(w).Encode(f.json())
		return
	}
	q := r.URL.Query().Get("q")
	g.mu.Lock()
	defer g.mu.Unlock()
	out := []map[string]any{}
	for _, f := range g.files {
		switch {
		case strings.Contains(q, "name = '"+DriveFolderName+"'"):
			if f.isDir && f.name == DriveFolderName {
				out = append(out, f.json())
			}
		case strings.Contains(q, "' in parents"):
			if !f.isDir && strings.HasPrefix(q, "'"+f.parent+"'") {
				out = append(out, f.json())
			}
		}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"files": out})
}

func (g *fakeGoogle) find(id string) *fakeFile {
	for _, f := range g.files {
		if f.id == id {
			return f
		}
	}
	return nil
}

func (g *fakeGoogle) item(w http.ResponseWriter, r *http.Request) {
	if !g.authed(w, r) {
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/drive/v3/files/")
	g.mu.Lock()
	defer g.mu.Unlock()
	f := g.find(id)
	if f == nil {
		w.WriteHeader(404)
		return
	}
	if r.Method == http.MethodDelete {
		for i, x := range g.files {
			if x == f {
				g.files = append(g.files[:i], g.files[i+1:]...)
			}
		}
		w.WriteHeader(204)
		return
	}
	_, _ = w.Write(f.data)
}

func (g *fakeGoogle) upload(w http.ResponseWriter, r *http.Request) {
	if !g.authed(w, r) {
		return
	}
	mt, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "multipart/related" {
		w.WriteHeader(400)
		return
	}
	mr := multipart.NewReader(r.Body, params["boundary"])
	var meta struct {
		Name    string            `json:"name"`
		Parents []string          `json:"parents"`
		Props   map[string]string `json:"appProperties"`
	}
	p1, _ := mr.NextPart()
	b1, _ := io.ReadAll(p1)
	_ = json.Unmarshal(b1, &meta)
	p2, _ := mr.NextPart()
	data, _ := io.ReadAll(p2)
	g.mu.Lock()
	defer g.mu.Unlock()
	var f *fakeFile
	if r.Method == http.MethodPatch {
		if f = g.find(strings.TrimPrefix(r.URL.Path, "/upload/drive/v3/files/")); f == nil {
			w.WriteHeader(404)
			return
		}
	} else {
		if len(meta.Parents) != 1 || !strings.HasPrefix(meta.Parents[0], "dir") {
			w.WriteHeader(400)
			return
		}
		g.next++
		f = &fakeFile{id: "id" + strconv.Itoa(g.next), parent: meta.Parents[0]}
		g.files = append(g.files, f)
	}
	f.name, f.data, f.props = meta.Name, data, meta.Props
	_ = json.NewEncoder(w).Encode(f.json())
}

func connectedDrive(t *testing.T, g *fakeGoogle) (*Drive, *memTokens) {
	t.Helper()
	tk := &memTokens{}
	d := NewDrive(g.cfg(), tk)
	authURL, err := d.AuthURL("http://127.0.0.1:8000/cb")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(authURL)
	q := u.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("scope") != googleScope || q.Get("access_type") != "offline" {
		t.Fatalf("параметры авторизации: %v", q)
	}
	if err := d.Complete(context.Background(), q.Get("state"), "good-code", "http://127.0.0.1:8000/cb"); err != nil {
		t.Fatal(err)
	}
	return d, tk
}

func TestDriveAuthFlow(t *testing.T) {
	g := newFakeGoogle(t)
	d, tk := connectedDrive(t, g)
	if tk.v != "ref-1" || !d.Connected() {
		t.Fatalf("токен не сохранён: %q", tk.v)
	}
	if g.lastForm.Get("client_secret") != "sec" || g.lastForm.Get("redirect_uri") != "http://127.0.0.1:8000/cb" {
		t.Fatalf("обмен кода: %v", g.lastForm)
	}
	// state одноразовый и чужой не принимается
	if err := d.Complete(context.Background(), "nope", "good-code", "x"); !errors.Is(err, ErrBadState) {
		t.Fatalf("чужой state: %v", err)
	}
	// после перезапуска приложения подключение берётся из сохранённого токена
	d2 := NewDrive(g.cfg(), tk)
	if !d2.Connected() {
		t.Fatal("после перезапуска не подключён")
	}
	if err := d2.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := d.Disconnect(); err != nil || d.Connected() || tk.v != "" {
		t.Fatalf("Disconnect: %v %q", err, tk.v)
	}
}

func TestDriveNotConfigured(t *testing.T) {
	d := NewDrive(GoogleConfig{}, &memTokens{})
	if d.Configured() || d.Connected() {
		t.Fatal("без клиента считается настроенным")
	}
	if _, err := d.AuthURL("x"); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("AuthURL: %v", err)
	}
	if _, err := d.List(context.Background()); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("List: %v", err)
	}
}

func TestDriveStateExpires(t *testing.T) {
	g := newFakeGoogle(t)
	d := NewDrive(g.cfg(), &memTokens{})
	authURL, _ := d.AuthURL("http://127.0.0.1:8000/cb")
	u, _ := url.Parse(authURL)
	d.now = func() time.Time { return time.Now().Add(authStateTTL + time.Second) }
	if err := d.Complete(context.Background(), u.Query().Get("state"), "good-code", "x"); !errors.Is(err, ErrBadState) {
		t.Fatalf("просроченный state: %v", err)
	}
}

func TestDrivePutGetListDeleteAndConflicts(t *testing.T) {
	g := newFakeGoogle(t)
	d, _ := connectedDrive(t, g)
	ctx := context.Background()
	const name = "oc-settings-pc.json"

	r, err := d.Put(ctx, name, []byte(`{"v":1}`), "")
	if err != nil || r.Fingerprint != Fingerprint([]byte(`{"v":1}`)) || r.Name != name || r.Size != 7 {
		t.Fatalf("Put: %+v %v", r, err)
	}
	if _, err := d.Put(ctx, name, []byte("two"), ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("перезапись без expect: %v", err)
	}
	if _, err := d.Put(ctx, name, []byte("two"), Fingerprint([]byte("other"))); !errors.Is(err, ErrConflict) {
		t.Fatalf("перезапись с чужим отпечатком: %v", err)
	}
	if _, err := d.Put(ctx, "oc-settings-new.json", []byte("x"), r.Fingerprint); !errors.Is(err, ErrConflict) {
		t.Fatalf("expect для несуществующего: %v", err)
	}
	r2, err := d.Put(ctx, name, []byte("two"), r.Fingerprint)
	if err != nil || r2.ID != r.ID || r2.Fingerprint != Fingerprint([]byte("two")) {
		t.Fatalf("замена известной версии: %+v %v", r2, err)
	}
	if n := len(g.files); n != 2 { // папка и один файл
		t.Fatalf("после замены записей: %d", n)
	}
	list, err := d.List(ctx)
	if err != nil || len(list) != 1 || list[0].ID != r.ID {
		t.Fatalf("List: %+v %v", list, err)
	}
	if got, err := d.Get(ctx, r.ID); err != nil || string(got) != "two" {
		t.Fatalf("Get: %q %v", got, err)
	}
	if _, err := d.Get(ctx, "../etc"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get с путём: %v", err)
	}
	if _, err := d.Get(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get несуществующего: %v", err)
	}
	if err := d.Delete(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if err := d.Delete(ctx, r.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("повторный Delete: %v", err)
	}
	if _, err := d.Put(ctx, "../x.json", []byte("x"), ""); err == nil {
		t.Fatal("плохое имя принято")
	}
}

func TestDriveRefreshesStaleAccessToken(t *testing.T) {
	g := newFakeGoogle(t)
	d, _ := connectedDrive(t, g)
	g.mu.Lock()
	g.staleOnce = true
	g.mu.Unlock()
	if _, err := d.List(context.Background()); err != nil {
		t.Fatalf("после 401 токен должен обновиться: %v", err)
	}
	if g.refreshes != 1 {
		t.Fatalf("обновлений токена: %d", g.refreshes)
	}
}

func TestDriveRevokedAccessDisconnects(t *testing.T) {
	g := newFakeGoogle(t)
	d, tk := connectedDrive(t, g)
	d.mu.Lock()
	d.access = "" // access-токен истёк: нужен refresh
	d.mu.Unlock()
	g.mu.Lock()
	g.revoked = true
	g.mu.Unlock()
	if _, err := d.List(context.Background()); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("List при отозванном доступе: %v", err)
	}
	if d.Connected() || tk.v != "" {
		t.Fatal("токен не забыт после отзыва доступа")
	}
}
