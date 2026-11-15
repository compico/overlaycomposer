package wasmplugins

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Пределы исходящих запросов плагина: плагин — не прокси и не качалка.
const (
	fetchTimeout  = 15 * time.Second
	fetchMaxBody  = 1 << 20
	localMaxBody  = 8 << 20 // ответ программы на этом компьютере (например, файл карты со сториборд)
	fetchMaxReq   = 256 << 10
	fetchMaxInfly = 8
)

var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// blockedAddr — адреса, куда плагину ходить нельзя: loopback, частные сети, link-local, multicast, CGNAT.
func blockedAddr(a netip.Addr) bool {
	a = a.Unmap()
	return !a.IsValid() || a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() ||
		a.IsMulticast() || a.IsUnspecified() || cgnat.Contains(a)
}

// publicOnly проверяет адрес уже после DNS, в момент соединения: так нельзя обойти запрет подменой DNS (rebinding).
func publicOnly(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return err
	}
	if blockedAddr(ip) {
		return fmt.Errorf("address %s is not allowed for plugins", ip)
	}
	return nil
}

// loopbackOnly пускает соединение только на loopback (проверка после DNS): цель net.local не должна вести дальше этого компьютера.
func loopbackOnly(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return err
	}
	if !ip.Unmap().IsLoopback() {
		return fmt.Errorf("address %s is not a loopback address", ip)
	}
	return nil
}

// newLocalClient — клиент для целей net.local: только loopback и без редиректов (редирект мог бы увести на другой адрес).
func newLocalClient() *http.Client {
	d := &net.Dialer{Timeout: 5 * time.Second, Control: loopbackOnly}
	return &http.Client{
		Transport:     &http.Transport{DialContext: d.DialContext, Proxy: nil, DisableKeepAlives: true},
		Timeout:       fetchTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// normLocalTarget приводит host:port к виду для сравнения; ok — только для 127.0.0.1, localhost и [::1] с портом 1..65535.
func normLocalTarget(s string) (string, bool) {
	host, port, err := net.SplitHostPort(strings.TrimSpace(s))
	if err != nil {
		return "", false
	}
	host = strings.ToLower(host)
	switch host {
	case "127.0.0.1", "localhost", "::1":
	default:
		return "", false
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 || port != strconv.Itoa(n) {
		return "", false
	}
	return net.JoinHostPort(host, port), true
}

// urlHostPort — host:port адреса запроса в виде normLocalTarget (порт по умолчанию — по схеме); "" — не разобрать.
func urlHostPort(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return ""
	}
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	n, ok := normLocalTarget(net.JoinHostPort(u.Hostname(), port))
	if !ok {
		return ""
	}
	return n
}

func newFetchClient(allowLocal bool) *http.Client {
	d := &net.Dialer{Timeout: 10 * time.Second}
	if !allowLocal {
		d.Control = publicOnly
	}
	tr := &http.Transport{
		DialContext:         d.DialContext,
		Proxy:               nil, // прокси мог бы обойти проверку адреса
		TLSHandshakeTimeout: 10 * time.Second,
		DisableKeepAlives:   true,
	}
	return &http.Client{
		Transport: tr,
		Timeout:   fetchTimeout,
		CheckRedirect: func(_ *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return errors.New("too many redirects")
			}
			return nil
		},
	}
}

type fetchArgs struct {
	ID     string `json:"id"`
	Method string `json:"method"`
	URL    string `json:"url"`
	Body   string `json:"body"`
}

type fetchResult struct {
	ID     string `json:"id"`
	Status int    `json:"status,omitempty"`
	Body   string `json:"body,omitempty"`
	Error  string `json:"error,omitempty"`
}

func (a *fetchArgs) validate() error {
	if a.ID == "" || len(a.ID) > 64 {
		return errors.New("id: 1..64 characters")
	}
	if a.Method == "" {
		a.Method = http.MethodGet
	}
	a.Method = strings.ToUpper(a.Method)
	switch a.Method {
	case "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD":
	default:
		return fmt.Errorf("method %s is not allowed", a.Method)
	}
	u, err := url.Parse(a.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("url: absolute http(s) URL expected")
	}
	if len(a.Body) > fetchMaxReq {
		return fmt.Errorf("body is too large (max %d KB)", fetchMaxReq>>10)
	}
	return nil
}

// do выполняет запрос; ошибка сети — в Error, а не в возврате: плагин получает её событием так же, как ответ.
func (a fetchArgs) do(ctx context.Context, c *http.Client, maxBody int64) fetchResult {
	res := fetchResult{ID: a.ID}
	req, err := http.NewRequestWithContext(ctx, a.Method, a.URL, bytes.NewReader([]byte(a.Body)))
	if err != nil {
		res.Error = err.Error()
		return res
	}
	req.Header.Set("User-Agent", "OverlayComposer-plugin")
	resp, err := c.Do(req)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	res.Status, res.Body = resp.StatusCode, string(b)
	return res
}
