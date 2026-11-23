package chat

import (
	"reflect"
	"testing"
)

func TestBuildPartsSplitsTextAndEmotes(t *testing.T) {
	text := "Привет! Kappa :) Kappa"
	emotes := []Emote{{"25", 17, 21}, {"25", 8, 12}, {"1902", 14, 15}}
	got := buildParts(text, emotes)
	want := []Part{
		{Text: "Привет! "},
		{Emote: "25", Name: "Kappa", URL: "https://static-cdn.jtvnw.net/emoticons/v2/25/default/dark/3.0"},
		{Text: " "},
		{Emote: "1902", Name: ":)", URL: "https://static-cdn.jtvnw.net/emoticons/v2/1902/default/dark/3.0"},
		{Text: " "},
		{Emote: "25", Name: "Kappa", URL: "https://static-cdn.jtvnw.net/emoticons/v2/25/default/dark/3.0"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parts:\n got %+v\nwant %+v", got, want)
	}
}

func TestBuildPartsIgnoresBadEmotes(t *testing.T) {
	// позиции вне текста, пересечение, перевёрнутый диапазон и id с «/» не должны ломать разбор или попасть в ссылку
	got := buildParts("Kappa hi", []Emote{{"25", 0, 4}, {"25", 3, 6}, {"25", 6, 2}, {"a/b", 6, 7}, {"7", 5, 99}})
	want := []Part{
		{Emote: "25", Name: "Kappa", URL: "https://static-cdn.jtvnw.net/emoticons/v2/25/default/dark/3.0"},
		{Text: " hi"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parts:\n got %+v\nwant %+v", got, want)
	}
}

func TestBuildPartsWithoutEmotes(t *testing.T) {
	if got := buildParts("просто текст", nil); !reflect.DeepEqual(got, []Part{{Text: "просто текст"}}) {
		t.Fatalf("parts: %+v", got)
	}
	if got := buildParts("", nil); got == nil || len(got) != 0 {
		t.Fatalf("пустой текст — пустой, но не nil список (JSON []): %#v", got)
	}
}

func TestEmoteURLRejectsOddIDs(t *testing.T) {
	for _, id := range []string{"", "a/b", "../x", "a b", "x?y"} {
		if EmoteURL(id) != "" {
			t.Fatalf("id %q не должен давать ссылку", id)
		}
	}
	if EmoteURL("emotesv2_abc123") == "" {
		t.Fatal("настоящий id вида emotesv2_… должен работать")
	}
}
