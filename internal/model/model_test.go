package model

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestIDAndSlug(t *testing.T) {
	for id, ok := range map[string]bool{"gaming": true, "a_b-1": true, "": false, "../x": false, "a b": false, strings.Repeat("a", 65): false} {
		if IsValidID(id) != ok {
			t.Errorf("IsValidID(%q) != %v", id, ok)
		}
	}
	if Slugify("  Starting Soon!! ") != "starting-soon" || Slugify("???") != "layout" {
		t.Error("slugify")
	}
}

func TestSafeURL(t *testing.T) {
	for u, ok := range map[string]bool{"https://a.com/x": true, "http://a": true, "/widgets/clock": true, "//evil.com": false, "javascript:alert(1)": false, "": false, "ftp://a": false, "https://": false} {
		if SafeURL(u) != ok {
			t.Errorf("SafeURL(%q) != %v", u, ok)
		}
	}
}

func TestNormalizeScenes(t *testing.T) {
	got := NormalizeScenes([]string{"A", "", " ", "B", "A", "C"})
	if !reflect.DeepEqual(got, []string{"A", "B", "C"}) {
		t.Errorf("got %v", got)
	}
	if NormalizeScenes(nil) == nil {
		t.Error("nil must become empty slice")
	}
}

func TestLayoutValidateJSONShape(t *testing.T) {
	l := Layout{ID: "x", Name: "X", Canvas: Canvas{1920, 1080}}
	if msg := l.Validate(); msg != "" {
		t.Fatal(msg)
	}
	b, _ := json.Marshal(l)
	if !strings.Contains(string(b), `"scenes":[]`) || !strings.Contains(string(b), `"objects":[]`) {
		t.Errorf("empty slices must serialize as []: %s", b)
	}
	bad := Layout{ID: "x", Name: "X"}
	if bad.Validate() != "invalid canvas" {
		t.Error("canvas validation")
	}
}
