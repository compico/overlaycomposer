package deck

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

func valid() Layout {
	return Layout{Rev: 3, Pages: []Page{
		{ID: "main", Name: "Main", Tiles: []Tile{
			{ID: "a", X: 0, Y: 0, W: 2, H: 2, Label: "Game", Color: "blue", Icon: "scene", Action: Action{Type: ActionScene, Scene: "Game"}},
			{ID: "b", X: 2, Y: 0, W: 1, H: 1, Label: "Ban", Confirm: true, Action: Action{Type: ActionScene, Scene: "Ban"}},
			{ID: "more", X: 7, Y: 5, W: 1, H: 1, Label: "More", Action: Action{Type: ActionPage, Page: "two"}},
		}},
		{ID: "two", Name: "Two"},
	}}
}

func TestValidateAcceptsGoodLayoutAndRejectsBadOnes(t *testing.T) {
	good := valid()
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	mut := func(f func(l *Layout)) error { l := valid(); f(&l); return l.Validate() }
	bad := map[string]func(l *Layout){
		"нет страниц":          func(l *Layout) { l.Pages = nil },
		"дубль страницы":       func(l *Layout) { l.Pages[1].ID = "main" },
		"плохой id страницы":   func(l *Layout) { l.Pages[1].ID = "Bad Id" },
		"пустое имя страницы":  func(l *Layout) { l.Pages[1].Name = "" },
		"дубль плитки":         func(l *Layout) { l.Pages[0].Tiles[1].ID = "a" },
		"плитка вне сетки":     func(l *Layout) { l.Pages[0].Tiles[2].X = 8 },
		"плитка шире края":     func(l *Layout) { l.Pages[0].Tiles[2].W = 2 },
		"нулевой размер":       func(l *Layout) { l.Pages[0].Tiles[0].W = 0 },
		"пересечение":          func(l *Layout) { l.Pages[0].Tiles[1].X = 1 },
		"отрицательный x":      func(l *Layout) { l.Pages[0].Tiles[1].X = -1 },
		"длинная подпись":      func(l *Layout) { l.Pages[0].Tiles[0].Label = strings.Repeat("я", MaxLabel+1) },
		"перевод строки":       func(l *Layout) { l.Pages[0].Tiles[0].Label = "a\nb" },
		"чужой цвет":           func(l *Layout) { l.Pages[0].Tiles[0].Color = "red;}</style><script>" },
		"чужая иконка":         func(l *Layout) { l.Pages[0].Tiles[0].Icon = "skull" },
		"неизвестное действие": func(l *Layout) { l.Pages[0].Tiles[0].Action.Type = "exec" },
		"страницы нет":         func(l *Layout) { l.Pages[0].Tiles[2].Action.Page = "nope" },
		"пустая сцена":         func(l *Layout) { l.Pages[0].Tiles[0].Action.Scene = "" },
		"слишком много страниц": func(l *Layout) {
			for i := 0; i < MaxPages; i++ {
				l.Pages = append(l.Pages, Page{ID: fmt.Sprintf("x%d", i), Name: "x"})
			}
		},
	}
	for name, f := range bad {
		if mut(f) == nil {
			t.Errorf("%s: должно отклоняться", name)
		}
	}
}

func TestParseRejectsUnknownFieldsAndOversize(t *testing.T) {
	if _, err := Parse([]byte(`{"pages":[{"id":"a","name":"A","tiles":[],"extra":1}]}`)); err == nil {
		t.Fatal("неизвестное поле")
	}
	if _, err := Parse([]byte(`{"pages":[{"id":"a","name":"A"}]}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := Parse([]byte(`nope`)); err == nil {
		t.Fatal("не JSON")
	}
	if _, err := Parse([]byte(strings.Repeat(" ", maxBytes+1))); err == nil {
		t.Fatal("слишком большой")
	}
}

func TestDefaultLayoutOneTilePerScene(t *testing.T) {
	var scenes []string
	for i := 0; i < 20; i++ {
		scenes = append(scenes, fmt.Sprintf("Scene %d", i))
	}
	scenes = append(scenes, "Scene 3") // дубль не плодит плитку
	l := Default(scenes)
	if err := l.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(l.Pages) != 3 || len(l.Pages[0].Tiles) != 8 || len(l.Pages[2].Tiles) != 4 {
		t.Fatalf("страницы: %d", len(l.Pages))
	}
	// id зависит от имени, а не от места: после перестановки сцен плитка указывает на ту же сцену
	a := Default([]string{"A", "B"})
	b := Default([]string{"B", "A"})
	ta, _ := a.Tile("scene-" + shortHash("A"))
	tb, _ := b.Tile("scene-" + shortHash("A"))
	if ta.Action.Scene != "A" || tb.Action.Scene != "A" {
		t.Fatal("плитка должна держаться за имя сцены")
	}
	if e := Default(nil); e.Validate() != nil || len(e.Pages[0].Tiles) != 0 {
		t.Fatal("без сцен — пустая страница, но раскладка валидна")
	}
	long := Default([]string{strings.Repeat("я", 300)})
	if long.Pages[0].Tiles[0].Action.Scene == "" || len([]rune(long.Pages[0].Tiles[0].Label)) > MaxLabel {
		t.Fatal("подпись обрезается, сцена остаётся как есть")
	}
}

func TestRenderServerSide(t *testing.T) {
	l := valid()
	l.Pages[0].Tiles[0].Label = `<script>alert(1)</script> "q"`
	out, err := Render(ViewData{Layout: l, PageID: "main", Scene: "Game", Online: true})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if strings.Contains(s, "<script>alert") {
		t.Fatal("подпись не экранирована")
	}
	for _, want := range []string{
		`data-rev="3"`, `data-id="a"`, `data-id="b"`, ` data-confirm="1"`, `href="/deck?page=two"`, `class="t c-blue on"`,
		`left:0.0000%;top:0.0000%;width:25.0000%;height:33.3333%`, `>Main</a>`, `>Two</a>`, `class="cur"`, `class="t c-steel"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("нет %q", want)
		}
	}
	if strings.Contains(s, `class="off"`) || strings.Contains(s, `class="t c-blue on off"`) {
		t.Fatal("онлайн")
	}
	// нет связи с OBS: активной сцены нет, страница помечена
	off, _ := Render(ViewData{Layout: l, PageID: "main", Scene: "Game", Online: false})
	if !strings.Contains(string(off), `<body class="off"`) || strings.Contains(string(off), "c-blue on") {
		t.Fatal("офлайн")
	}
	// неизвестная страница — первая; одна страница — без вкладок
	u, _ := Render(ViewData{Layout: l, PageID: "zzz"})
	if !strings.Contains(string(u), `data-id="a"`) {
		t.Fatal("неизвестная страница должна открывать первую")
	}
	one := Layout{Pages: []Page{{ID: "p1", Name: "Only"}}}
	o, _ := Render(ViewData{Layout: one})
	if strings.Contains(string(o), `>Only</a>`) {
		t.Fatal("вкладки не нужны, когда страница одна")
	}
	if _, err := Render(ViewData{}); err == nil {
		t.Fatal("без страниц — ошибка")
	}
}

// Клиентский скрипт должен работать в Safari 9: ES5, без стрелок, let/const, шаблонных строк, Promise/fetch и т. п.
func TestClientScriptIsES5(t *testing.T) {
	for _, bad := range []string{"=>", "`", "fetch(", "Promise", "async ", "await ", "class ", "...", "Symbol", "Object.assign", ".includes(", ".startsWith(", ".endsWith(", ".padStart", "Array.from", " of "} {
		if strings.Contains(clientJS, bad) {
			t.Errorf("не ES5: %q", bad)
		}
	}
	if m := regexp.MustCompile(`(^|[^\w.])(let|const)\s`).FindString(clientJS); m != "" {
		t.Errorf("let/const: %q", m)
	}
	// на страницу нет внешних адресов
	out, _ := Render(ViewData{Layout: valid()})
	if regexp.MustCompile(`(src|href)="https?:`).Match(out) || regexp.MustCompile(`\son[a-z]+="`).Match(out) {
		t.Fatal("внешние ресурсы или inline-обработчики")
	}
}
