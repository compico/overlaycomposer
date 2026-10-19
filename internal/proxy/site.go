package proxy

import (
	"net/url"
	"regexp"
	"strings"

	"github.com/compico/overlaycomposer/internal/model"
)

const (
	Prefix     = "/proxy/"    // /proxy/<id>/<путь>
	WSPrefix   = "/proxy-ws/" // /proxy-ws/<id>/<ws|wss>/<host>/<путь>
	HostPrefix = "/proxy-h/"  // /proxy-h/<id>/<http|https>/<host>/<путь>
)

// normOrigin — scheme://host[:port] в нижнем регистре, без порта по умолчанию (как URL.origin в браузере).
func normOrigin(u *url.URL) string {
	scheme, host := strings.ToLower(u.Scheme), strings.ToLower(u.Host)
	if (scheme == "http" && strings.HasSuffix(host, ":80")) || (scheme == "https" && strings.HasSuffix(host, ":443")) {
		host = host[:strings.LastIndex(host, ":")]
	}
	return scheme + "://" + host
}

// UpstreamOrigin — origin источника, если его можно проксировать, иначе "".
func UpstreamOrigin(src *model.Source) string {
	if src == nil || !src.Data.Proxy || src.Data.URL == "" {
		return ""
	}
	u, err := url.Parse(src.Data.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}
	return normOrigin(u)
}

var ipv4Re = regexp.MustCompile(`^\d+(\.\d+){3}$`)

// SiteOf — хост «сайта»: centrifugo.example.com → example.com (IP и localhost без изменений).
func SiteOf(hostname string) string {
	if ipv4Re.MatchString(hostname) || strings.Contains(hostname, ":") {
		return hostname
	}
	parts := strings.Split(hostname, ".")
	if len(parts) <= 2 {
		return hostname
	}
	// example.co.uk, example.com.au и т.п.
	second := parts[len(parts)-2]
	public := len(parts[len(parts)-1]) == 2
	switch second {
	case "co", "com", "org", "net", "gov", "edu", "ac":
	default:
		public = false
	}
	n := 2
	if public {
		n = 3
	}
	return strings.Join(parts[len(parts)-n:], ".")
}

func hostInSite(hostname, site string) bool {
	return hostname == site || strings.HasSuffix(hostname, "."+site)
}

// SiblingOrigin — origin соседнего хоста того же «сайта» (socket8.example.com для источника на www.example.com)
// или "", если хост чужой. Это единственная проверка, не дающая использовать /proxy-h/ как открытый прокси.
func SiblingOrigin(sourceOrigin, scheme, host string) string {
	if scheme != "http" && scheme != "https" {
		return ""
	}
	u, err := url.Parse(scheme + "://" + host)
	if err != nil || u.User != nil || u.Host == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return ""
	}
	src, err := url.Parse(sourceOrigin)
	if err != nil || !hostInSite(strings.ToLower(u.Hostname()), SiteOf(strings.ToLower(src.Hostname()))) {
		return ""
	}
	return normOrigin(u)
}

var refererRe = regexp.MustCompile(`^/proxy/([a-zA-Z0-9_-]{1,64})/`)

// RefererProxyID — id источника, если запрос пришёл со страницы, загруженной через /proxy/<id>/ на этом же хосте.
func RefererProxyID(referer, reqHost string) string {
	if referer == "" {
		return ""
	}
	r, err := url.Parse(referer)
	if err != nil || !strings.EqualFold(r.Host, reqHost) {
		return ""
	}
	if m := refererRe.FindStringSubmatch(r.Path); m != nil {
		return m[1]
	}
	return ""
}

// rewriteCookie: Set-Cookie от сайта → cookie для нашего origin, действующая только для mount (/proxy/<id> или /proxy-h/<id>/…).
// Убираем Domain (чужой домен браузер отбросит), Secure и SameSite (мы по http, и iframe у нас same-origin).
func rewriteCookie(raw, mount string) string {
	parts := strings.Split(raw, ";")
	out := []string{strings.TrimSpace(parts[0]), "Path=" + mount + "/", "SameSite=Lax"}
	for _, a := range parts[1:] {
		a = strings.TrimSpace(a)
		name := strings.ToLower(a)
		if i := strings.IndexByte(name, '='); i >= 0 {
			name = name[:i]
		}
		if name == "expires" || name == "max-age" || name == "httponly" {
			out = append(out, a)
		}
	}
	return strings.Join(out, "; ")
}
