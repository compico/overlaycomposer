// Package rewrite — правка чужого HTML/JS при проксировании.
//
// Виджеты (например DonationAlerts) проверяют «я во фрейме?» (`window.self !== window.top`) и в iframe
// не создают сокет. `top`/`parent` переопределить нельзя (unforgeable), поэтому такие сравнения переписываются
// прямо в тексте: `!=`/`!==` → `(false)`, `==`/`===` → `(true)`.
//
// В Go (RE2) нет lookbehind/lookahead, поэтому границы выражения («не часть чужого выражения»: obj.self,
// foo_top, self.location …) проверяются вручную по соседним символам.
package rewrite

import (
	"regexp"
	"strings"
)

const (
	op      = `\s*(!==?|===?)\s*`
	aSelf   = `(?:(?:window\.)?self|window)`
	aTop    = `(?:window\.)?top`
	aParent = `window\.parent`
	// location-проверки
	aLocFrame = `(?:(?:window\.)?top|window\.parent)\.location(?:\.href)?`
	aLocSelf  = `(?:(?:window|self|document)\.)?location(?:\.href)?`
	// обфусцированный вид: window['self'] !== window[_0x40f318(0xa5)]
	bSelf = `window(?:\.self|\[\s*['"]self['"]\s*\])`
	bTop  = `window(?:\.(?:top|parent)|\[[^\]\n]{1,80}\])`
)

type rule struct {
	re *regexp.Regexp
	// endBound — после совпадения не должно идти [\w$.] (иначе это начало более длинного выражения)
	endBound bool
}

func pair(a, b string, endBound bool) rule {
	return rule{regexp.MustCompile(a + op + b), endBound}
}

var rules = []rule{
	pair(aSelf, aTop, true), pair(aTop, aSelf, true),
	pair(aSelf, aParent, true), pair(aParent, aSelf, true),
	pair(aLocFrame, aLocSelf, true), pair(aLocSelf, aLocFrame, true),
	pair(bSelf, bTop, false), pair(bTop, bSelf, true),
}

var quick = regexp.MustCompile(`\b(?:top|parent|self)\b|\[\s*['"]self['"]`)

func isIdent(c byte) bool {
	return c == '_' || c == '$' || c == '.' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// NeutralizeFrameChecks заменяет проверки «страница во фрейме» константами. count — сколько мест найдено.
func NeutralizeFrameChecks(text string) (string, int) {
	if !quick.MatchString(text) {
		return text, 0
	}
	count := 0
	for _, r := range rules {
		ms := r.re.FindAllStringSubmatchIndex(text, -1)
		if len(ms) == 0 {
			continue
		}
		var b strings.Builder
		last := 0
		for _, m := range ms {
			start, end := m[0], m[1]
			if start > 0 && isIdent(text[start-1]) {
				continue // часть чужого выражения: obj.self, foo_top …
			}
			if r.endBound && end < len(text) && isIdent(text[end]) {
				continue // self.location, topLevel …
			}
			b.WriteString(text[last:start])
			if strings.HasPrefix(text[m[2]:m[3]], "!") {
				b.WriteString("(false)")
			} else {
				b.WriteString("(true)")
			}
			last = end
			count++
		}
		if last > 0 {
			b.WriteString(text[last:])
			text = b.String()
		}
	}
	return text, count
}
