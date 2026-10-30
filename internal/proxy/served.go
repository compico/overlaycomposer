package proxy

import (
	"net/url"
	"strings"
	"sync"
)

// servedPaths помнит пути, отданные через Referer-fallback (/overlay/assets/index.js → id источника).
//
// Зачем: ресурс, запрошенный страницей напрямую (<link href="/overlay/assets/app.css">), приходит с Referer
// /proxy/<id>/…, и мы знаем источник. Но у зависимостей этого ресурса (import './vendor.js' в модуле, url(font.woff)
// в CSS) Referer — уже URL самого модуля/стиля, то есть /overlay/assets/app.js без префикса /proxy/<id>/
// (Chromium/CEF так и делает, Firefox шлёт URL документа). Запоминая отданные пути, цепочку можно продолжить
// на любую глубину.
type servedPaths struct {
	mu sync.Mutex
	m  map[string]string
}

const maxServed = 4096

func (s *servedPaths) get(path string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m[path]
}

// set запоминает путь за источником; путь, уже закреплённый за другим источником, не переписывается
// (иначе цепочка зависимостей первого источника начала бы уходить ко второму).
func (s *servedPaths) set(path, id string) {
	if path == "" || strings.HasPrefix(path, "/proxy") {
		return
	}
	// Fiber отдаёт строки запроса поверх переиспользуемого буфера: храним копии, иначе после запроса ключи портятся.
	path, id = strings.Clone(path), strings.Clone(id)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil || len(s.m) >= maxServed {
		s.m = make(map[string]string)
	}
	if _, taken := s.m[path]; !taken {
		s.m[path] = id
	}
}

// refererID определяет источник по Referer запроса к нашему хосту: прямой /proxy/<id>/… либо путь,
// ранее отданный fallback-ом для этого источника. Прямое совпадение главнее.
func (p *Proxy) refererID(referer, reqHost string) string {
	if id := RefererProxyID(referer, reqHost); id != "" {
		return id
	}
	if referer == "" {
		return ""
	}
	r, err := url.Parse(referer)
	if err != nil || !strings.EqualFold(r.Host, reqHost) {
		return ""
	}
	return p.served.get(r.Path)
}
