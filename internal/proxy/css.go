package proxy

import "regexp"

var (
	cssURLRe    = regexp.MustCompile(`url\(\s*(["']?)(/[^"')\s]*)(["']?)\s*\)`)
	cssImportRe = regexp.MustCompile(`(@import\s+)(["'])(/[^"']*)(["'])`)
)

// RewriteCSS переписывает root-relative url(/font.woff) и @import "/a.css" на точку монтирования источника,
// чтобы стили и их зависимости находили свой origin без Referer.
func RewriteCSS(src []byte, mount string) []byte {
	if mount == "" {
		return src
	}
	out := cssURLRe.ReplaceAllFunc(src, func(m []byte) []byte {
		g := cssURLRe.FindSubmatch(m)
		nu, ok := mountURL(string(g[2]), mount)
		if !ok {
			return m
		}
		return []byte("url(" + string(g[1]) + nu + string(g[3]) + ")")
	})
	return cssImportRe.ReplaceAllFunc(out, func(m []byte) []byte {
		g := cssImportRe.FindSubmatch(m)
		nu, ok := mountURL(string(g[3]), mount)
		if !ok {
			return m
		}
		return []byte(string(g[1]) + string(g[2]) + nu + string(g[4]))
	})
}
