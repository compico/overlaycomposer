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
	"strings"
	"syscall"
	"time"
)

// Пределы исходящих запросов плагина: плагин — не прокси и не качалка.
const (
	fetchTimeout  = 15 * time.Second
	fetchMaxBody  = 1 << 20
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
func (a fetchArgs) do(ctx context.Context, c *http.Client) fetchResult {
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
	b, _ := io.ReadAll(io.LimitReader(resp.Body, fetchMaxBody))
	res.Status, res.Body = resp.StatusCode, string(b)
	return res
}
