package dashboard

import (
	"strings"
	"testing"
)

func TestParseAndValidate(t *testing.T) {
	ok := `{"rev":3,"panels":[{"id":"chat","plugin":"twitch-tools","widget":"chat","w":6,"h":6}]}`
	l, err := Parse([]byte(ok))
	if err != nil || l.Rev != 3 || len(l.Panels) != 1 {
		t.Fatalf("%+v %v", l, err)
	}
	if l, err := Parse([]byte(`{"rev":0}`)); err != nil || l.Panels == nil {
		t.Fatalf("пустая раскладка допустима и не null: %+v %v", l, err)
	}
	bad := map[string]string{
		"не json":          `{`,
		"неизвестное поле": `{"rev":0,"panels":[],"x":1}`,
		"id":               `{"panels":[{"id":"A","plugin":"p","widget":"w","w":1,"h":1}]}`,
		"повтор id":        `{"panels":[{"id":"a","plugin":"p","widget":"w","w":1,"h":1},{"id":"a","plugin":"p","widget":"w","w":1,"h":1}]}`,
		"плагин":           `{"panels":[{"id":"a","plugin":"","widget":"w","w":1,"h":1}]}`,
		"ширина 0":         `{"panels":[{"id":"a","plugin":"p","widget":"w","w":0,"h":1}]}`,
		"ширина 13":        `{"panels":[{"id":"a","plugin":"p","widget":"w","w":13,"h":1}]}`,
		"высота 9":         `{"panels":[{"id":"a","plugin":"p","widget":"w","w":1,"h":9}]}`,
		"слишком большая":  `{"panels":[],"pad":"` + strings.Repeat("a", maxBytes) + `"}`,
	}
	for name, raw := range bad {
		if _, err := Parse([]byte(raw)); err == nil {
			t.Errorf("%s: ожидалась ошибка", name)
		}
	}
	var many []string
	for i := 0; i <= MaxPanels; i++ {
		many = append(many, `{"id":"p`+strings.Repeat("a", i)+`","plugin":"p","widget":"w","w":1,"h":1}`)
	}
	if _, err := Parse([]byte(`{"panels":[` + strings.Join(many, ",") + `]}`)); err == nil {
		t.Error("панелей больше предела")
	}
}

func TestDefault(t *testing.T) {
	l := Default([]Widget{{Plugin: "twitch-tools", Widget: "chat", Name: "Twitch chat"}})
	if len(l.Panels) != 1 || l.Panels[0].W != Cols/2 || l.Validate() != nil {
		t.Fatalf("%+v", l)
	}
	if l := Default(nil); l.Panels == nil || l.Validate() != nil {
		t.Fatalf("пустая раскладка по умолчанию: %+v", l)
	}
	var many []Widget
	for i := 0; i < MaxPanels+5; i++ {
		many = append(many, Widget{Plugin: "p", Widget: "w" + strings.Repeat("a", i)})
	}
	if l := Default(many); len(l.Panels) != MaxPanels || l.Validate() != nil {
		t.Fatalf("по умолчанию не больше %d панелей", MaxPanels)
	}
}
