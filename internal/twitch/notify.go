package twitch

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/compico/overlaycomposer/internal/notify"
)

var roleTitle = map[string]string{RoleStreamer: "Streamer", RoleBot: "Bot"}

// ReconnectItems — уведомления «аккаунт нужно подключить заново» для центра уведомлений (notify.Center.Sync).
// Ключ зависит от причины: сменилась причина (был сбой токена, теперь не хватает прав) — это новое уведомление.
func ReconnectItems(accs []Account) []notify.Item {
	var out []notify.Item
	for _, a := range accs {
		annotate(&a)
		if !a.NeedsReconnect {
			continue
		}
		who := roleTitle[a.Role]
		if who == "" {
			who = a.Role
		}
		var key, text string
		if a.LastError != "" {
			key, text = "twitch:"+a.Role+":token", a.LastError
		} else {
			miss := append([]string(nil), a.MissingScopes...)
			sort.Strings(miss)
			key = "twitch:" + a.Role + ":scopes:" + strings.Join(miss, ",")
			text = "New features need extra permissions: " + strings.Join(miss, ", ") + ". Connect the account again to grant them."
		}
		data, _ := json.Marshal(map[string]any{"role": a.Role, "login": a.Login, "missingScopes": a.MissingScopes})
		out = append(out, notify.Item{
			Key: key, Kind: notify.KindTwitchReconnect,
			Title: "Reconnect Twitch: " + who + " (" + a.Login + ")", Text: text, Data: data,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}
