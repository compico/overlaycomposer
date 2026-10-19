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

func TestObjectInstanceID(t *testing.T) {
	l := Layout{ID: "l", Name: "L", Canvas: Canvas{Width: 1920, Height: 1080}, Objects: []Object{{ID: "o1", SourceID: "s", InstanceID: "inst_ab12"}}}
	if msg := l.Validate(); msg != "" {
		t.Fatalf("валидный instanceId отклонён: %s", msg)
	}
	b, err := json.Marshal(l.Objects[0])
	if err != nil || !strings.Contains(string(b), `"instanceId":"inst_ab12"`) {
		t.Fatalf("instanceId потерян при сериализации: %s %v", b, err)
	}
	var back Object
	if err := json.Unmarshal(b, &back); err != nil || back.InstanceID != "inst_ab12" {
		t.Fatalf("instanceId потерян при чтении: %+v %v", back, err)
	}
	l.Objects[0].InstanceID = "bad id/../"
	if msg := l.Validate(); msg == "" {
		t.Fatal("невалидный instanceId должен отклоняться")
	}
	if b, _ := json.Marshal(Object{ID: "o2"}); strings.Contains(string(b), "instanceId") {
		t.Fatalf("пустой instanceId не должен попадать в JSON: %s", b)
	}
}

func TestTransformCrop(t *testing.T) {
	mk := func(tr Transform) Layout {
		return Layout{ID: "l", Name: "L", Canvas: Canvas{Width: 1920, Height: 1080}, Objects: []Object{{ID: "o", SourceID: "s", Transform: tr}}}
	}
	ok := mk(Transform{Width: 400, Height: 300, CropLeft: 100, CropBottom: 50})
	if msg := ok.Validate(); msg != "" {
		t.Fatalf("валидная обрезка отклонена: %s", msg)
	}
	for name, tr := range map[string]Transform{
		"отрицательная":    {Width: 400, Height: 300, CropLeft: -1},
		"съела всю ширину": {Width: 400, Height: 300, CropLeft: 200, CropRight: 200},
		"съела всю высоту": {Width: 400, Height: 300, CropTop: 300},
	} {
		l := mk(tr)
		if msg := l.Validate(); msg != "invalid crop" {
			t.Errorf("%s: Validate() = %q, want invalid crop", name, msg)
		}
	}
	zero := mk(Transform{}) // старые данные без размера и обрезки остаются валидными
	if msg := zero.Validate(); msg != "" {
		t.Errorf("пустой transform: %s", msg)
	}
	b, _ := json.Marshal(Transform{Width: 1, Height: 1})
	if strings.Contains(string(b), "crop") {
		t.Errorf("нулевая обрезка не должна попадать в JSON: %s", b)
	}
}
