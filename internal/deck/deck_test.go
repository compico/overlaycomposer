package deck

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

func scene(id string, x, y, w, h int, label string) Tile {
	return Tile{ID: id, X: x, Y: y, W: w, H: h, Label: label, Color: "blue", Action: Action{Type: ActionScene, Scene: label}}
}

func valid() Layout {
	return Layout{Rev: 3, Groups: []Group{
		{ID: "main", Name: "Main", Tiles: []Tile{
			scene("a", 0, 0, 2, 1, "Game"),
			{ID: "b", X: 2, Y: 0, W: 1, H: 1, Label: "Ban", Confirm: true, Icon: "ban", Action: Action{Type: ActionScene, Scene: "Ban"}},
			scene("big", 3, 0, 2, 3, "Big"),
			scene("wide", 0, 1, 3, 1, "Wide"),
		}},
		{ID: "two", Name: ""},
	}}
}

func TestValidateAcceptsGoodLayoutAndRejectsBadOnes(t *testing.T) {
	good := valid()
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	mut := func(f func(l *Layout)) error { l := valid(); f(&l); return l.Validate() }
	bad := map[string]func(l *Layout){
		"нет групп":               func(l *Layout) { l.Groups = nil },
		"дубль группы":            func(l *Layout) { l.Groups[1].ID = "main" },
		"плохой id группы":        func(l *Layout) { l.Groups[1].ID = "Bad Id" },
		"длинное имя группы":      func(l *Layout) { l.Groups[1].Name = strings.Repeat("я", MaxName+1) },
		"перевод строки в имени":  func(l *Layout) { l.Groups[1].Name = "a\nb" },
		"дубль плитки":            func(l *Layout) { l.Groups[0].Tiles[1].ID = "a" },
		"дубль плитки в группах":  func(l *Layout) { l.Groups[1].Tiles = []Tile{scene("a", 0, 0, 1, 1, "X")} },
		"размер 2×2 не из набора": func(l *Layout) { l.Groups[0].Tiles[3] = scene("wide", 0, 3, 2, 2, "W") },
		"размер 1×2 не из набора": func(l *Layout) { l.Groups[0].Tiles[1].H = 2 },
		"нулевой размер":          func(l *Layout) { l.Groups[0].Tiles[1].W = 0 },
		"ниже последнего ряда":    func(l *Layout) { l.Groups[0].Tiles[2].Y = 2 }, // 2×3 с y=2 вылезает за 4 ряда
		"правее предела группы":   func(l *Layout) { l.Groups[0].Tiles[1].X = MaxGroupCols },
		"отрицательный x":         func(l *Layout) { l.Groups[0].Tiles[1].X = -1 },
		"пересечение":             func(l *Layout) { l.Groups[0].Tiles[1].X = 1 },
		"длинная подпись":         func(l *Layout) { l.Groups[0].Tiles[0].Label = strings.Repeat("я", MaxLabel+1) },
		"перевод строки":          func(l *Layout) { l.Groups[0].Tiles[0].Label = "a\nb" },
		"чужой цвет":              func(l *Layout) { l.Groups[0].Tiles[0].Color = "red;}</style><script>" },
		"чужая иконка":            func(l *Layout) { l.Groups[0].Tiles[0].Icon = "skull" },
		"неизвестное действие":    func(l *Layout) { l.Groups[0].Tiles[0].Action.Type = "exec" },
		"пустая сцена":            func(l *Layout) { l.Groups[0].Tiles[0].Action.Scene = "" },
		"слишком много групп": func(l *Layout) {
			for i := 0; i < MaxGroups; i++ {
				l.Groups = append(l.Groups, Group{ID: fmt.Sprintf("x%d", i)})
			}
		},
	}
	for name, f := range bad {
		if mut(f) == nil {
			t.Errorf("%s: должно отклоняться", name)
		}
	}
	// разрешённые размеры
	for _, s := range Sizes {
		l := Layout{Groups: []Group{{ID: "g", Tiles: []Tile{scene("t", 0, 0, s.W, s.H, "S")}}}}
		if err := l.Validate(); err != nil {
			t.Errorf("%dx%d: %v", s.W, s.H, err)
		}
	}
}

func TestParseRejectsUnknownFieldsAndOversize(t *testing.T) {
	if _, err := Parse([]byte(`{"groups":[{"id":"a","tiles":[],"extra":1}]}`)); err == nil {
		t.Fatal("неизвестное поле")
	}
	if _, err := Parse([]byte(`{"pages":[{"id":"a","name":"A"}]}`)); err == nil {
		t.Fatal("старый формат со страницами не принимается")
	}
	if _, err := Parse([]byte(`{"groups":[{"id":"a"}]}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := Parse([]byte(`nope`)); err == nil {
		t.Fatal("не JSON")
	}
	if _, err := Parse([]byte(strings.Repeat(" ", maxBytes+1))); err == nil {
		t.Fatal("слишком большой")
	}
}

func TestGroupWidthFollowsTiles(t *testing.T) {
	l := valid()
	if w := l.Groups[0].Width(); w != 5 {
		t.Fatalf("ширина = самая правая плитка: %d", w)
	}
	if w := l.Groups[1].Width(); w != 1 {
		t.Fatalf("пустая группа занимает клетку: %d", w)
	}
}

func TestDefaultLayoutWideTilesInColumnsOfFour(t *testing.T) {
	var scenes []string
	for i := 0; i < 20; i++ {
		scenes = append(scenes, fmt.Sprintf("Scene %d", i))
	}
	scenes = append(scenes, "Scene 3") // дубль не плодит плитку
	l := Default(scenes)
	if err := l.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(l.Groups) != 3 || len(l.Groups[0].Tiles) != 8 || len(l.Groups[2].Tiles) != 4 {
		t.Fatalf("группы: %d", len(l.Groups))
	}
	if l.Groups[0].Name != "Scenes" || l.Groups[1].Name != "Scenes 2" {
		t.Fatalf("имена: %q %q", l.Groups[0].Name, l.Groups[1].Name)
	}
	// порядок как в OBS; столбцы по 4 плитки 2×1: первая колонка сверху вниз, затем вторая
	g := l.Groups[0]
	pos := func(i int) [2]int { return [2]int{g.Tiles[i].X, g.Tiles[i].Y} }
	if g.Tiles[0].Action.Scene != "Scene 0" || pos(0) != [2]int{0, 0} || pos(3) != [2]int{0, 3} || pos(4) != [2]int{2, 0} || pos(7) != [2]int{2, 3} {
		t.Fatalf("позиции: %v %v %v %v", pos(0), pos(3), pos(4), pos(7))
	}
	if g.Width() != 4 {
		t.Fatalf("ширина группы: %d", g.Width())
	}
	// id зависит от имени, а не от места
	a, b := Default([]string{"A", "B"}), Default([]string{"B", "A"})
	ta, _ := a.Tile("scene-" + shortHash("A"))
	tb, _ := b.Tile("scene-" + shortHash("A"))
	if ta.Action.Scene != "A" || tb.Action.Scene != "A" {
		t.Fatal("плитка должна держаться за имя сцены")
	}
	if e := Default(nil); e.Validate() != nil || len(e.Groups[0].Tiles) != 0 {
		t.Fatal("без сцен — пустая группа, но раскладка валидна")
	}
	long := Default([]string{strings.Repeat("я", 300)})
	if long.Groups[0].Tiles[0].Action.Scene == "" || len([]rune(long.Groups[0].Tiles[0].Label)) > MaxLabel {
		t.Fatal("подпись обрезается, сцена остаётся как есть")
	}
}

func TestRenderServerSide(t *testing.T) {
	l := valid()
	l.Groups[0].Tiles[0].Label = `<script>alert(1)</script> "q"`
	out, err := Render(ViewData{Layout: l, Scene: "Game", Online: true})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if strings.Contains(s, "<script>alert") {
		t.Fatal("подпись не экранирована")
	}
	// группа 1: x=0.5, ширина 5; группа 2: x=0.5+5+0.6=6.1; полоса: 6.1+1+0.5=7.6
	for _, want := range []string{
		`data-rev="3"`, `data-id="a"`, `data-id="big"`, ` data-confirm="1"`, `class="gn">Main</div>`,
		`left:0.50em;width:5.00em`, `left:6.10em;width:1.00em`, `width:7.60em`,
		`left:0em;top:0.50em;width:2em;height:1em`, // 2×1 в левом верхнем углу под заголовком
		`left:3em;top:0.50em;width:2em;height:3em`, // крупная 2×3
		`left:0em;top:1.50em;width:3em;height:1em`, // 3×1 во втором ряду
		`class="t c-steel"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("нет %q", want)
		}
	}
	// подсветка активной сцены: плитка «Game» из примера названа через подпись, поэтому проверяем по data-scene
	l.Groups[0].Tiles[0] = scene("a", 0, 0, 2, 1, "Game")
	on, _ := Render(ViewData{Layout: l, Scene: "Game", Online: true})
	if !strings.Contains(string(on), `class="t c-blue on"`) {
		t.Fatal("активная сцена подсвечивается")
	}
	off, _ := Render(ViewData{Layout: l, Scene: "Game", Online: false})
	if !strings.Contains(string(off), `<body class="off"`) || strings.Contains(string(off), "c-blue on") {
		t.Fatal("офлайн: активной сцены нет, страница помечена")
	}
	if _, err := Render(ViewData{}); err == nil {
		t.Fatal("без групп — ошибка")
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
	out, _ := Render(ViewData{Layout: valid()})
	if regexp.MustCompile(`(src|href)="https?:`).Match(out) || regexp.MustCompile(`\son[a-z]+="`).Match(out) {
		t.Fatal("внешние ресурсы или inline-обработчики")
	}
	// страница не опирается на то, чего нет в Safari 9: CSS-переменные, grid, vh-расчёты в calc
	css := string(out)
	for _, bad := range []string{"var(--", "display:grid", "grid-template", "position:sticky", "scroll-snap"} {
		if strings.Contains(css, bad) {
			t.Errorf("Safari 9 не умеет: %q", bad)
		}
	}
}
