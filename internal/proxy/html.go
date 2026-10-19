package proxy

import (
	"bytes"
	"strings"

	"golang.org/x/net/html"

	"github.com/compico/overlaycomposer/internal/rewrite"
)

// Stats — что сделали с документом.
type Stats struct {
	Neutralized int // скрытых проверок «страница во фрейме»
	CSPRemoved  int // удалённых <meta http-equiv="Content-Security-Policy">
	Remounted   int // переписанных root-relative URL (/a.css → /proxy/<id>/a.css)
}

var jsTypes = map[string]bool{
	"": true, "module": true, "text/javascript": true, "application/javascript": true, "application/x-javascript": true,
	"text/ecmascript": true, "application/ecmascript": true, "text/jscript": true, "text/livescript": true,
}

// RewriteHTML потоково (токенайзер x/net/html, без построения DOM) проходит документ:
//   - вставляет inject в начало <head> — но после начальных <meta charset>/<meta http-equiv=content-type>,
//     которые обязаны остаться в первых 1024 байтах;
//   - скрывает проверки «я во фрейме?» в тексте JS-скриптов и в атрибутах-обработчиках;
//   - убирает <meta http-equiv="Content-Security-Policy"> (иначе он запретит наш inline-скрипт);
//   - если задан mount (/proxy/<id>), root-relative URL в href/src/action/poster/srcset/… получают этот префикс:
//     так ресурсы находят свой источник без Referer (CEF/Chromium присылает его не всегда так, как Firefox).
//
// Остальные токены копируются байт в байт, поэтому документ не нормализуется (в отличие от DOM-парсера + рендера).
func RewriteHTML(src []byte, inject, mount string) ([]byte, Stats) {
	var st Stats
	z := html.NewTokenizer(bytes.NewReader(src))
	var out bytes.Buffer
	out.Grow(len(src) + len(inject) + 64)

	inserted, waitMeta, sawStartTag, inScript := false, false, false, false
	var scratch []byte
	insert := func() {
		if !inserted {
			out.WriteString(inject)
			inserted, waitMeta = true, false
		}
	}

	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			break
		}
		scratch = append(scratch[:0], z.Raw()...) // Raw меняется после TagName/TagAttr — копируем
		raw := scratch

		switch tt {
		case html.TextToken:
			if inScript {
				s, n := rewrite.NeutralizeFrameChecks(string(raw))
				st.Neutralized += n
				out.WriteString(s)
				continue
			}
			if waitMeta && len(bytes.TrimSpace(raw)) > 0 {
				insert()
			}
			out.Write(raw)

		case html.CommentToken, html.DoctypeToken:
			out.Write(raw)

		case html.ErrorToken:
			// обработан выше (конец документа); ветка нужна только для полноты switch

		case html.StartTagToken, html.SelfClosingTagToken:
			nameB, hasAttr := z.TagName()
			name := string(nameB)
			attrs := map[string]string{}
			var all []attr
			for hasAttr {
				k, v, more := z.TagAttr()
				all = append(all, attr{string(k), string(v)})
				switch string(k) {
				case "type", "http-equiv", "charset":
					attrs[string(k)] = string(v)
				}
				hasAttr = more
			}
			sawStartTag = true

			charsetMeta := name == "meta" && (hasKey(attrs, "charset") || strings.EqualFold(strings.TrimSpace(attrs["http-equiv"]), "content-type"))
			if waitMeta && !charsetMeta {
				insert()
			}
			if !inserted && !waitMeta {
				switch name {
				case "html":
					// вставим перед следующим тегом: либо после <head> и его charset, либо перед <body>
				case "head":
					out.Write(raw)
					waitMeta = true
					continue
				default:
					insert()
				}
			}
			if name == "meta" {
				switch strings.ToLower(strings.TrimSpace(attrs["http-equiv"])) {
				case "content-security-policy", "content-security-policy-report-only":
					st.CSPRemoved++
					continue
				}
			}
			if mount != "" {
				if nraw, n := remountTag(name, tt == html.SelfClosingTagToken, all, mount); n > 0 {
					raw = nraw
					st.Remounted += n
				}
			}
			if name == "script" && tt == html.StartTagToken {
				inScript = jsTypes[strings.ToLower(strings.TrimSpace(attrs["type"]))]
				out.Write(raw)
				continue
			}
			s, n := rewrite.NeutralizeFrameChecks(string(raw)) // атрибуты-обработчики: onclick="if(window.self!==window.top)…"
			st.Neutralized += n
			out.WriteString(s)

		case html.EndTagToken:
			nameB, _ := z.TagName()
			name := string(nameB)
			if name == "script" {
				inScript = false
			}
			if waitMeta {
				insert() // пустой <head></head>
			}
			out.Write(raw)
		}
	}
	if !inserted {
		if sawStartTag {
			out.WriteString(inject) // документ закончился, не дойдя до подходящего места
		} else {
			return append([]byte(inject), out.Bytes()...), st
		}
	}
	return out.Bytes(), st
}

func hasKey(m map[string]string, k string) bool { _, ok := m[k]; return ok }

type attr struct{ k, v string }

var urlAttrs = map[string]bool{"href": true, "src": true, "action": true, "poster": true, "formaction": true, "data-src": true}

// mountURL: "/a/b" → mount+"/a/b"; всё остальное (относительные, абсолютные, //host, уже наши) без изменений.
func mountURL(v, mount string) (string, bool) {
	if v == "" || v[0] != '/' {
		return v, false
	}
	if len(v) > 1 && (v[1] == '/' || v[1] == '\\') {
		return v, false
	}
	if strings.HasPrefix(v, mount+"/") || strings.HasPrefix(v, "/proxy/") || strings.HasPrefix(v, "/proxy-h/") || strings.HasPrefix(v, "/proxy-ws/") {
		return v, false
	}
	return mount + v, true
}

func mountSrcset(v, mount string) (string, bool) {
	parts := strings.Split(v, ",")
	changed := false
	for i, p := range parts {
		t := strings.TrimSpace(p)
		u, rest, _ := strings.Cut(t, " ")
		if nu, ok := mountURL(u, mount); ok {
			parts[i] = " " + nu
			if rest != "" {
				parts[i] += " " + rest
			}
			changed = true
		}
	}
	if !changed {
		return v, false
	}
	return strings.TrimSpace(strings.Join(parts, ",")), true
}

// remountTag пересобирает тег, если в нём есть root-relative URL; возвращает число замен.
func remountTag(name string, selfClosing bool, attrs []attr, mount string) ([]byte, int) {
	n := 0
	out := make([]attr, len(attrs))
	for i, a := range attrs {
		out[i] = a
		switch {
		case urlAttrs[a.k]:
			if nv, ok := mountURL(a.v, mount); ok {
				out[i].v = nv
				n++
			}
		case a.k == "srcset":
			if nv, ok := mountSrcset(a.v, mount); ok {
				out[i].v = nv
				n++
			}
		}
	}
	if n == 0 {
		return nil, 0
	}
	var b strings.Builder
	b.WriteByte('<')
	b.WriteString(name)
	for _, a := range out {
		b.WriteByte(' ')
		b.WriteString(a.k)
		b.WriteString(`="`)
		b.WriteString(html.EscapeString(a.v))
		b.WriteByte('"')
	}
	if selfClosing {
		b.WriteString(" /")
	}
	b.WriteByte('>')
	return []byte(b.String()), n
}
