package chat

import "strings"

const (
	maxCommandName = 32
	maxCommandArgs = 16
	maxCommandArg  = 200
)

// ParseCommand разбирает сообщение вида «!np арг1 арг2»: name — команда без «!» в нижнем регистре, args — слова после неё
// (разделитель — пробел, регистр сохраняется). ok=false, если это не команда: нет «!» в начале, пустое или слишком длинное имя,
// в имени не только латиница, цифры и «_» («!!!», «! np», «!привет» — не команды).
func ParseCommand(text string) (name string, args []string, ok bool) {
	text = strings.TrimSpace(text)
	if len(text) < 2 || text[0] != '!' {
		return "", nil, false
	}
	words := strings.Fields(text[1:])
	if len(words) == 0 || text[1] == ' ' || text[1] == '\t' {
		return "", nil, false
	}
	name = strings.ToLower(words[0])
	if len(name) > maxCommandName {
		return "", nil, false
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_') {
			return "", nil, false
		}
	}
	args = []string{}
	for _, w := range words[1:] {
		if len(args) == maxCommandArgs {
			break
		}
		if len(w) > maxCommandArg {
			w = w[:maxCommandArg]
		}
		args = append(args, w)
	}
	return name, args, true
}
