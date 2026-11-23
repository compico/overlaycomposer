package cloud

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// OAuth-клиент Google (тип «Desktop») встроен в приложение: подставляется при сборке (-ldflags "-X .../cloud.GoogleClientID=...").
// Секрет такого клиента секретом не является (Google это допускает для установленных приложений); защита — PKCE и loopback-редирект.
var (
	GoogleClientID     string
	GoogleClientSecret string
)

// drive.appdata: скрытая папка приложения (appDataFolder). Файлы не видны в интерфейсе Drive и доступны только этому
// OAuth-клиенту; остальных файлов пользователя приложение не видит. Scope несекретный (проверка приложения не нужна).
const googleScope = "https://www.googleapis.com/auth/drive.appdata"

// appData — псевдо-id папки приложения в Drive API.
const appData = "appDataFolder"

var (
	// ErrNotConfigured — в этой сборке нет OAuth-клиента Google.
	ErrNotConfigured = errors.New("Google Drive is not available in this build (no OAuth client)")
	// ErrScope — у сохранённого токена нет нужного разрешения (подключали с другим scope): подключиться заново.
	ErrScope = errors.New("Google Drive access is out of date: connect Google Drive again")
	// ErrBadState — неизвестный или просроченный state: подключение нужно начать заново.
	ErrBadState = errors.New("unknown or expired state, start the connection again")
)

// GoogleConfig — адреса Google и OAuth-клиент; пустые адреса — настоящие (подмена нужна тестам).
type GoogleConfig struct {
	ClientID, ClientSecret string
	AuthURL, TokenURL      string
	APIURL, UploadURL      string
}

func (c GoogleConfig) withDefaults() GoogleConfig {
	def := func(p *string, v string) {
		if *p == "" {
			*p = v
		}
	}
	def(&c.AuthURL, "https://accounts.google.com/o/oauth2/v2/auth")
	def(&c.TokenURL, "https://oauth2.googleapis.com/token")
	def(&c.APIURL, "https://www.googleapis.com/drive/v3")
	def(&c.UploadURL, "https://www.googleapis.com/upload/drive/v3")
	return c
}

// TokenStore — где лежит refresh-токен (в приложении — зашифрованно). Пустая строка — токена нет.
type TokenStore interface {
	Load() (string, error)
	Save(token string) error
}

// Drive — провайдер Google Drive (скрытая папка приложения, scope drive.appdata).
type Drive struct {
	cfg    GoogleConfig
	http   *http.Client
	tokens TokenStore
	now    func() time.Time

	mu      sync.Mutex
	refresh string // загруженный refresh-токен
	loaded  bool
	access  string
	expires time.Time
	pending map[string]pendingAuth
}

type pendingAuth struct {
	verifier string
	expires  time.Time
}

const authStateTTL = 10 * time.Minute

func NewDrive(cfg GoogleConfig, tokens TokenStore) *Drive {
	return &Drive{cfg: cfg.withDefaults(), http: &http.Client{Timeout: 30 * time.Second}, tokens: tokens, now: time.Now, pending: map[string]pendingAuth{}}
}

func (d *Drive) Name() string { return "google" }

// Configured — есть ли в сборке OAuth-клиент.
func (d *Drive) Configured() bool { return d.cfg.ClientID != "" }

func (d *Drive) loadToken() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.loaded {
		d.loaded = true
		if t, err := d.tokens.Load(); err == nil {
			d.refresh = t
		}
	}
	return d.refresh
}

func (d *Drive) Connected() bool { return d.Configured() && d.loadToken() != "" }

func randToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// AuthURL начинает подключение: адрес страницы согласия Google. redirect — адрес этого приложения, куда Google вернёт code.
func (d *Drive) AuthURL(redirect string) (string, error) {
	if !d.Configured() {
		return "", ErrNotConfigured
	}
	state, verifier := randToken(16), randToken(32)
	sum := sha256.Sum256([]byte(verifier))
	d.mu.Lock()
	now := d.now()
	for k, p := range d.pending {
		if now.After(p.expires) {
			delete(d.pending, k)
		}
	}
	d.pending[state] = pendingAuth{verifier: verifier, expires: now.Add(authStateTTL)}
	d.mu.Unlock()
	q := url.Values{
		"client_id":             {d.cfg.ClientID},
		"redirect_uri":          {redirect},
		"response_type":         {"code"},
		"scope":                 {googleScope},
		"state":                 {state},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(sum[:])},
		"code_challenge_method": {"S256"},
		"access_type":           {"offline"},
		"prompt":                {"consent"}, // без него Google не выдаёт refresh-токен повторно
	}
	return d.cfg.AuthURL + "?" + q.Encode(), nil
}

// Complete завершает подключение: меняет code на токены и сохраняет refresh-токен. state одноразовый.
func (d *Drive) Complete(ctx context.Context, state, code, redirect string) error {
	d.mu.Lock()
	p, ok := d.pending[state]
	delete(d.pending, state)
	expired := ok && d.now().After(p.expires)
	d.mu.Unlock()
	if !ok || expired {
		return ErrBadState
	}
	tok, err := d.token(ctx, url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "code_verifier": {p.verifier}, "redirect_uri": {redirect},
	})
	if err != nil {
		return err
	}
	if !hasScope(tok.Scope, googleScope) {
		return errors.New("Google Drive access was not granted: tick the permission to store app data and connect again")
	}
	if tok.Refresh == "" {
		return errors.New("Google did not return a refresh token; remove the app in your Google account permissions and connect again")
	}
	if err := d.tokens.Save(tok.Refresh); err != nil {
		return err
	}
	d.mu.Lock()
	d.refresh, d.loaded = tok.Refresh, true
	d.access, d.expires = tok.Access, d.now().Add(time.Duration(tok.ExpiresIn)*time.Second)
	d.mu.Unlock()
	return nil
}

type tokenResp struct {
	Access    string `json:"access_token"`
	Refresh   string `json:"refresh_token"`
	Scope     string `json:"scope"`
	ExpiresIn int    `json:"expires_in"`
	Error     string `json:"error"`
}

func (d *Drive) token(ctx context.Context, form url.Values) (tokenResp, error) {
	form.Set("client_id", d.cfg.ClientID)
	form.Set("client_secret", d.cfg.ClientSecret)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.cfg.TokenURL, bytes.NewReader([]byte(form.Encode())))
	if err != nil {
		return tokenResp{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := d.http.Do(req)
	if err != nil {
		return tokenResp{}, err
	}
	defer resp.Body.Close()
	var t tokenResp
	_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&t)
	if resp.StatusCode != http.StatusOK || t.Access == "" {
		if t.Error == "invalid_grant" {
			return t, ErrNotConnected
		}
		return t, fmt.Errorf("Google token request failed (%d %s)", resp.StatusCode, t.Error)
	}
	return t, nil
}

// accessToken — действующий access-токен (обновляется по refresh-токену).
func (d *Drive) accessToken(ctx context.Context, force bool) (string, error) {
	if !d.Configured() {
		return "", ErrNotConfigured
	}
	refresh := d.loadToken()
	if refresh == "" {
		return "", ErrNotConnected
	}
	d.mu.Lock()
	if !force && d.access != "" && d.now().Before(d.expires.Add(-time.Minute)) {
		a := d.access
		d.mu.Unlock()
		return a, nil
	}
	d.mu.Unlock()
	tok, err := d.token(ctx, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}})
	if errors.Is(err, ErrNotConnected) {
		_ = d.Disconnect() // доступ отозван в аккаунте Google: подключаться заново
	}
	if err != nil {
		return "", err
	}
	d.mu.Lock()
	d.access, d.expires = tok.Access, d.now().Add(time.Duration(tok.ExpiresIn)*time.Second)
	d.mu.Unlock()
	return tok.Access, nil
}

// Connect проверяет, что подключение рабочее (токен обновляется). Само подключение — AuthURL и Complete.
func (d *Drive) Connect(ctx context.Context) error {
	_, err := d.accessToken(ctx, true)
	return err
}

func (d *Drive) Disconnect() error {
	d.mu.Lock()
	d.refresh, d.loaded, d.access = "", true, ""
	d.mu.Unlock()
	return d.tokens.Save("")
}

// call выполняет запрос к API с Bearer-токеном; при 401 один раз обновляет токен.
func (d *Drive) call(ctx context.Context, method, u, contentType string, body []byte) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		tok, err := d.accessToken(ctx, attempt > 0)
		if err != nil {
			return nil, err
		}
		var rdr io.Reader
		if body != nil {
			rdr = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, u, rdr)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		resp, err := d.http.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			resp.Body.Close()
			continue
		}
		if resp.StatusCode == http.StatusForbidden {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<12))
			resp.Body.Close()
			if bytes.Contains(b, []byte("insufficientScopes")) {
				_ = d.Disconnect() // токен выдан с другим scope: он бесполезен, подключиться нужно заново
				return nil, ErrScope
			}
			return nil, fmt.Errorf("Google Drive: 403 %s", bytes.TrimSpace(b))
		}
		return resp, nil
	}
}

func apiErr(resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<12))
	if resp.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	return fmt.Errorf("Google Drive: %d %s", resp.StatusCode, bytes.TrimSpace(b))
}

type driveFile struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Size         string            `json:"size"`
	ModifiedTime string            `json:"modifiedTime"`
	AppProps     map[string]string `json:"appProperties"`
}

func (f driveFile) remote() Remote {
	size, _ := strconv.ParseInt(f.Size, 10, 64)
	t, _ := time.Parse(time.RFC3339, f.ModifiedTime)
	return Remote{ID: f.ID, Name: f.Name, Size: size, ModTime: t, Fingerprint: f.AppProps["sha256"]}
}

const fileFields = "id,name,size,modifiedTime,appProperties"

// query — files.list с запросом q; fields — поля файла.
func (d *Drive) query(ctx context.Context, q, order string) ([]driveFile, error) {
	v := url.Values{"spaces": {appData}, "q": {q}, "orderBy": {order}, "pageSize": {"200"}, "fields": {"files(" + fileFields + ")"}}
	resp, err := d.call(ctx, http.MethodGet, d.cfg.APIURL+"/files?"+v.Encode(), "", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, apiErr(resp)
	}
	var out struct {
		Files []driveFile `json:"files"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&out); err != nil {
		return nil, err
	}
	return out.Files, nil
}

// list — файлы бандлов в папке приложения.
func (d *Drive) list(ctx context.Context) ([]driveFile, error) {
	return d.query(ctx, "trashed = false", "modifiedTime desc")
}

func (d *Drive) List(ctx context.Context) ([]Remote, error) {
	files, err := d.list(ctx)
	if err != nil {
		return nil, err
	}
	out := []Remote{}
	for _, f := range files {
		if ValidName(f.Name) == nil {
			out = append(out, f.remote())
		}
	}
	return out, nil
}

var driveIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

func (d *Drive) Get(ctx context.Context, id string) ([]byte, error) {
	if !driveIDRe.MatchString(id) {
		return nil, ErrNotFound
	}
	resp, err := d.call(ctx, http.MethodGet, d.cfg.APIURL+"/files/"+id+"?alt=media", "", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, apiErr(resp)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxFileSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxFileSize {
		return nil, fmt.Errorf("the file is larger than %d MB", MaxFileSize>>20)
	}
	return data, nil
}

func (d *Drive) Put(ctx context.Context, name string, data []byte, expect string) (Remote, error) {
	if err := ValidName(name); err != nil {
		return Remote{}, err
	}
	if len(data) > MaxFileSize {
		return Remote{}, fmt.Errorf("the file is larger than %d MB", MaxFileSize>>20)
	}
	files, err := d.list(ctx)
	if err != nil {
		return Remote{}, err
	}
	var cur *driveFile
	for i := range files {
		if files[i].Name == name {
			cur = &files[i]
			break
		}
	}
	switch {
	case cur == nil && expect != "":
		return Remote{}, ErrConflict
	case cur != nil && (expect == "" || cur.AppProps["sha256"] != expect):
		return Remote{}, ErrConflict
	}
	meta := map[string]any{"name": name, "appProperties": map[string]string{"sha256": Fingerprint(data)}}
	method, u := http.MethodPost, d.cfg.UploadURL+"/files?uploadType=multipart&fields="+url.QueryEscape(fileFields)
	if cur == nil {
		meta["parents"] = []string{appData}
	} else {
		method, u = http.MethodPatch, d.cfg.UploadURL+"/files/"+cur.ID+"?uploadType=multipart&fields="+url.QueryEscape(fileFields)
	}
	body, ctype, err := relatedBody(meta, data)
	if err != nil {
		return Remote{}, err
	}
	resp, err := d.call(ctx, method, u, ctype, body)
	if err != nil {
		return Remote{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Remote{}, apiErr(resp)
	}
	var f driveFile
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&f); err != nil {
		return Remote{}, err
	}
	return f.remote(), nil
}

// relatedBody собирает multipart/related: метаданные JSON и содержимое.
func relatedBody(meta any, data []byte) ([]byte, string, error) {
	raw, err := json.Marshal(meta)
	if err != nil {
		return nil, "", err
	}
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for _, p := range []struct {
		ctype string
		b     []byte
	}{{"application/json; charset=UTF-8", raw}, {"application/json", data}} {
		part, err := w.CreatePart(textproto.MIMEHeader{"Content-Type": {p.ctype}})
		if err != nil {
			return nil, "", err
		}
		if _, err := part.Write(p.b); err != nil {
			return nil, "", err
		}
	}
	if err := w.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), "multipart/related; boundary=" + w.Boundary(), nil
}

func (d *Drive) Delete(ctx context.Context, id string) error {
	if !driveIDRe.MatchString(id) {
		return ErrNotFound
	}
	resp, err := d.call(ctx, http.MethodDelete, d.cfg.APIURL+"/files/"+id, "", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		return apiErr(resp)
	}
	return nil
}

// hasScope — есть ли want в списке scope из ответа Google (разделены пробелами). Пустой список (старые ответы) не считается отказом.
func hasScope(granted, want string) bool {
	if granted == "" {
		return true
	}
	for _, s := range strings.Fields(granted) {
		if s == want {
			return true
		}
	}
	return false
}
