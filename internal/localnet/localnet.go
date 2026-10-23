// Package localnet — запросы плагинов к программам на этом же компьютере (например, tosu).
//
// Плагин живёт в sandbox-iframe, и браузер отправляет из него запросы с «Origin: null»; многие локальные программы
// такие запросы отвергают. Сервер OC делает запрос сам (без Origin/Referer), но только к loopback-адресу и только GET.
package localnet

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	MaxBody = 1 << 20 // 1 МБ
	Timeout = 3 * time.Second
)

var (
	ErrTarget = errors.New("target must be host:port on this computer (127.0.0.1, localhost or [::1])")
	ErrPath   = errors.New("path must start with / and contain no spaces")
	ErrBig    = errors.New("response is too large")
)

// Result — ответ локальной программы.
type Result struct {
	Status int    `json:"status"`
	Body   string `json:"body"`
}

// Target проверяет «host:port» и возвращает его в виде, пригодном для URL. Разрешён только loopback.
func Target(s string) (string, error) {
	host, port, err := net.SplitHostPort(s)
	if err != nil {
		return "", ErrTarget
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return "", ErrTarget
	}
	switch strings.ToLower(host) {
	case "localhost", "127.0.0.1":
		return "127.0.0.1:" + port, nil
	case "::1":
		return "[::1]:" + port, nil
	}
	return "", ErrTarget
}

// Get делает GET http://<target><path> без Origin/Referer и без переходов по редиректам.
func Get(ctx context.Context, target, path string) (Result, error) {
	t, err := Target(target)
	if err != nil {
		return Result{}, err
	}
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || len(path) > 512 || strings.ContainsAny(path, " \r\n\t\\") {
		return Result{}, ErrPath
	}
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+t+path, nil)
	if err != nil {
		return Result{}, ErrPath
	}
	cl := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := cl.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("not reachable: %w", errors.Unwrap(err))
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, MaxBody+1))
	if err != nil {
		return Result{}, err
	}
	if len(b) > MaxBody {
		return Result{}, ErrBig
	}
	return Result{Status: resp.StatusCode, Body: string(b)}, nil
}
