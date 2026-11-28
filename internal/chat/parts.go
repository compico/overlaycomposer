package chat

import (
	"regexp"
	"sort"
)

const emoteBase = "https://static-cdn.jtvnw.net/emoticons/v2/"

var emoteIDRe = regexp.MustCompile(`^[A-Za-z0-9_]{1,64}$`)

// Part — кусок сообщения: обычный текст (Text) или смайл Twitch (Emote) с готовой ссылкой на картинку.
// Потребителю не нужно считать позиции в тексте: достаточно пройти по Parts и вывести каждый кусок.
type Part struct {
	Text  string `json:"text,omitempty"`
	Emote string `json:"emote,omitempty"` // id смайла Twitch
	Name  string `json:"name,omitempty"`  // как смайл написан в тексте («Kappa»)
	URL   string `json:"url,omitempty"`   // картинка 3.0 для тёмного фона; анимированная, если смайл анимированный
}

// EmoteURL — ссылка на картинку смайла Twitch по id. Пустая строка, если id не похож на id смайла.
func EmoteURL(id string) string {
	if !emoteIDRe.MatchString(id) {
		return ""
	}
	return emoteBase + id + "/default/dark/3.0"
}

// buildParts режет текст на куски по позициям смайлов (позиции — в символах Unicode, включительно). Смайлы с негодным
// id или позициями вне текста и пересекающиеся остаются обычным текстом.
func buildParts(text string, emotes []Emote) []Part {
	runes := []rune(text)
	sorted := append([]Emote(nil), emotes...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Start < sorted[j].Start })
	parts := []Part{}
	pos := 0
	for _, e := range sorted {
		if e.Start < pos || e.End < e.Start || e.End >= len(runes) || EmoteURL(e.ID) == "" {
			continue
		}
		if e.Start > pos {
			parts = append(parts, Part{Text: string(runes[pos:e.Start])})
		}
		parts = append(parts, Part{Emote: e.ID, Name: string(runes[e.Start : e.End+1]), URL: EmoteURL(e.ID)})
		pos = e.End + 1
	}
	if pos < len(runes) {
		parts = append(parts, Part{Text: string(runes[pos:])})
	}
	return parts
}
