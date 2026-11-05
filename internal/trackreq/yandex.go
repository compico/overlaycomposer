package trackreq

import (
	"net/url"
	"regexp"
	"strings"
)

// Track — трек Яндекс Музыки, разобранный из ссылки зрителя.
type Track struct {
	AlbumID string `json:"albumId"`
	TrackID string `json:"trackId"`
	URL     string `json:"url"` // каноническая ссылка без query и якоря
}

var yandexHosts = map[string]bool{"music.yandex.ru": true, "music.yandex.com": true, "music.yandex.kz": true, "music.yandex.by": true}

var trackPathRe = regexp.MustCompile(`^/album/(\d{1,20})/track/(\d{1,20})/?$`)

const maxInput = 2048

// ParseYandexTrack строго разбирает ссылку вида https://music.yandex.ru/album/{id}/track/{id}.
// Ввод зрителя недоверенный: принимаются только https, хосты из списка и именно такой путь; ID из произвольного текста не вытаскиваем.
func ParseYandexTrack(input string) (Track, bool) {
	if len(input) > maxInput {
		return Track{}, false
	}
	u, err := url.Parse(strings.TrimSpace(input))
	if err != nil || u.Scheme != "https" || u.User != nil {
		return Track{}, false
	}
	host := strings.ToLower(u.Hostname())
	if !yandexHosts[host] || (u.Port() != "" && u.Port() != "443") {
		return Track{}, false
	}
	m := trackPathRe.FindStringSubmatch(u.Path)
	if m == nil {
		return Track{}, false
	}
	return Track{AlbumID: m[1], TrackID: m[2], URL: "https://" + host + "/album/" + m[1] + "/track/" + m[2]}, true
}
