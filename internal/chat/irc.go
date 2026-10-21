package chat

import (
	"strconv"
	"strings"
)

// ircLine — разобранная строка IRC (с тегами IRCv3, как их отдаёт Twitch).
type ircLine struct {
	Tags   map[string]string
	Prefix string
	Cmd    string
	Params []string // последний параметр — «хвост» после " :"
}

func parseLine(s string) (ircLine, bool) {
	var l ircLine
	s = strings.TrimRight(s, "\r\n")
	if s == "" {
		return l, false
	}
	if s[0] == '@' {
		sp := strings.IndexByte(s, ' ')
		if sp < 0 {
			return l, false
		}
		l.Tags = parseTags(s[1:sp])
		s = strings.TrimLeft(s[sp+1:], " ")
	}
	if s != "" && s[0] == ':' {
		sp := strings.IndexByte(s, ' ')
		if sp < 0 {
			return l, false
		}
		l.Prefix = s[1:sp]
		s = strings.TrimLeft(s[sp+1:], " ")
	}
	sp := strings.IndexByte(s, ' ')
	if sp < 0 {
		l.Cmd = s
		return l, l.Cmd != ""
	}
	l.Cmd = s[:sp]
	s = strings.TrimLeft(s[sp+1:], " ")
	for s != "" {
		if s[0] == ':' {
			l.Params = append(l.Params, s[1:])
			break
		}
		sp = strings.IndexByte(s, ' ')
		if sp < 0 {
			l.Params = append(l.Params, s)
			break
		}
		l.Params = append(l.Params, s[:sp])
		s = strings.TrimLeft(s[sp+1:], " ")
	}
	return l, l.Cmd != ""
}

func parseTags(s string) map[string]string {
	m := map[string]string{}
	for _, kv := range strings.Split(s, ";") {
		if kv == "" {
			continue
		}
		k, v, _ := strings.Cut(kv, "=")
		m[k] = unescapeTag(v)
	}
	return m
}

func unescapeTag(v string) string {
	if !strings.Contains(v, `\`) {
		return v
	}
	var b strings.Builder
	for i := 0; i < len(v); i++ {
		if v[i] != '\\' || i+1 >= len(v) {
			if v[i] != '\\' {
				b.WriteByte(v[i])
			}
			continue
		}
		i++
		switch v[i] {
		case ':':
			b.WriteByte(';')
		case 's':
			b.WriteByte(' ')
		case 'r':
			b.WriteByte('\r')
		case 'n':
			b.WriteByte('\n')
		default: // \\ и неизвестные — сам символ
			b.WriteByte(v[i])
		}
	}
	return b.String()
}

// Emote — смайл в тексте. Start/End — позиции в символах (code points) текста сообщения, End включительно.
type Emote struct {
	ID    string `json:"id"`
	Start int    `json:"start"`
	End   int    `json:"end"`
}

// Message — сообщение чата.
type Message struct {
	ID            string   `json:"id"`
	Channel       string   `json:"channel"`
	UserID        string   `json:"userId"`
	Login         string   `json:"login"`
	DisplayName   string   `json:"displayName"`
	Color         string   `json:"color"`
	Text          string   `json:"text"`
	Action        bool     `json:"action"` // /me
	Badges        []string `json:"badges"` // имена значков: broadcaster, moderator, subscriber, vip, …
	IsBroadcaster bool     `json:"isBroadcaster"`
	IsMod         bool     `json:"isMod"`
	IsVip         bool     `json:"isVip"`
	IsSub         bool     `json:"isSub"`
	FirstMsg      bool     `json:"firstMsg"`
	Emotes        []Emote  `json:"emotes"`
	ReplyTo       string   `json:"replyTo,omitempty"` // id сообщения, на которое это ответ
	Timestamp     int64    `json:"timestamp"`         // мс
	FromBot       bool     `json:"fromBot"`           // написано подключённым аккаунтом бота (чтобы не реагировать на себя)
}

func parseEmotes(s string) []Emote {
	out := []Emote{}
	if s == "" {
		return out
	}
	for _, part := range strings.Split(s, "/") {
		id, ranges, ok := strings.Cut(part, ":")
		if !ok || id == "" {
			continue
		}
		for _, r := range strings.Split(ranges, ",") {
			a, b, ok := strings.Cut(r, "-")
			if !ok {
				continue
			}
			from, e1 := strconv.Atoi(a)
			to, e2 := strconv.Atoi(b)
			if e1 != nil || e2 != nil || from < 0 || to < from {
				continue
			}
			out = append(out, Emote{ID: id, Start: from, End: to})
		}
	}
	return out
}

// messageFrom строит Message из PRIVMSG. ok=false — строка не похожа на сообщение чата.
func messageFrom(l ircLine) (Message, bool) {
	if l.Cmd != "PRIVMSG" || len(l.Params) < 2 || !strings.HasPrefix(l.Params[0], "#") {
		return Message{}, false
	}
	t := l.Tags
	m := Message{
		ID: t["id"], Channel: strings.ToLower(l.Params[0][1:]), UserID: t["user-id"],
		Login: login(l.Prefix), DisplayName: t["display-name"], Color: t["color"],
		Text: l.Params[1], Badges: []string{}, FirstMsg: t["first-msg"] == "1", ReplyTo: t["reply-parent-msg-id"],
		Emotes: parseEmotes(t["emotes"]),
	}
	if m.DisplayName == "" {
		m.DisplayName = m.Login
	}
	if ms, err := strconv.ParseInt(t["tmi-sent-ts"], 10, 64); err == nil {
		m.Timestamp = ms
	}
	if strings.HasPrefix(m.Text, "\x01ACTION ") && strings.HasSuffix(m.Text, "\x01") {
		m.Action = true
		m.Text = strings.TrimSuffix(strings.TrimPrefix(m.Text, "\x01ACTION "), "\x01")
	}
	for _, b := range strings.Split(t["badges"], ",") {
		name, _, _ := strings.Cut(b, "/")
		if name == "" {
			continue
		}
		m.Badges = append(m.Badges, name)
		switch name {
		case "broadcaster":
			m.IsBroadcaster = true
		case "moderator":
			m.IsMod = true
		case "vip":
			m.IsVip = true
		case "subscriber", "founder":
			m.IsSub = true
		}
	}
	if t["mod"] == "1" {
		m.IsMod = true
	}
	if t["subscriber"] == "1" {
		m.IsSub = true
	}
	return m, true
}

// login достаёт ник из префикса «nick!user@host».
func login(prefix string) string {
	nick, _, _ := strings.Cut(prefix, "!")
	return strings.ToLower(nick)
}
