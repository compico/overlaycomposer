package trackreq

import (
	"strings"
	"testing"
)

func TestParseYandexTrack(t *testing.T) {
	ok := map[string]Track{
		"https://music.yandex.ru/album/33658663/track/132062255":    {"33658663", "132062255", "https://music.yandex.ru/album/33658663/track/132062255"},
		"  https://music.yandex.ru/album/1/track/2/  ":              {"1", "2", "https://music.yandex.ru/album/1/track/2"},
		"https://MUSIC.YANDEX.COM/album/1/track/2?utm_source=web#x": {"1", "2", "https://music.yandex.com/album/1/track/2"},
		"https://music.yandex.kz:443/album/5/track/6":               {"5", "6", "https://music.yandex.kz/album/5/track/6"},
	}
	for in, want := range ok {
		if got, good := ParseYandexTrack(in); !good || got != want {
			t.Errorf("%q: %+v %v", in, got, good)
		}
	}
}

func TestParseYandexTrackSadPaths(t *testing.T) {
	bad := []string{
		"", "hello", "music.yandex.ru/album/1/track/2",
		"http://music.yandex.ru/album/1/track/2",                      // не https
		"https://example.com/album/1/track/2",                         // чужой хост
		"https://music.yandex.ru.evil.com/album/1/track/2",            // похожий хост
		"https://evil.com@music.yandex.ru/album/1/track/2",            // userinfo
		"https://music.yandex.ru:8443/album/1/track/2",                // чужой порт
		"https://music.yandex.ru/album/1",                             // альбом, не трек
		"https://music.yandex.ru/album/1/track/2/extra",               // лишний путь
		"https://music.yandex.ru/album/x/track/2",                     // не числа
		"https://music.yandex.ru/album/1/track/2 и ещё текст",         // текст вокруг
		"look https://music.yandex.ru/album/1/track/2",                // ID из произвольного текста не вытаскиваем
		"https://music.yandex.ru/album/123456789012345678901/track/2", // слишком длинный ID
		"https://music.yandex.ru/album/1/track/2?" + strings.Repeat("a", 2100),
	}
	for _, in := range bad {
		if tr, ok := ParseYandexTrack(in); ok {
			t.Errorf("%q принят: %+v", in, tr)
		}
	}
}
