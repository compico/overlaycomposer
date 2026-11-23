package deck

import (
	"encoding/json"
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
		"mute без источника":      func(l *Layout) { l.Groups[0].Tiles[0].Action = Action{Type: ActionMute} },
		"неизвестный вид группы":  func(l *Layout) { l.Groups[1].Kind = "board" },
		"cols у обычной группы":   func(l *Layout) { l.Groups[1].Cols = 4 },
		"микшер с плитками": func(l *Layout) {
			l.Groups[1] = Group{ID: "m", Kind: GroupMixer, Cols: 8, Tiles: []Tile{scene("z", 0, 0, 1, 1, "Z")}}
		},
		"микшер слишком узкий":   func(l *Layout) { l.Groups[1] = Group{ID: "m", Kind: GroupMixer, Cols: MixerMinCols - 1} },
		"микшер слишком широкий": func(l *Layout) { l.Groups[1] = Group{ID: "m", Kind: GroupMixer, Cols: MaxGroupCols + 1} },
		"два микшера": func(l *Layout) {
			l.Groups = append(l.Groups, Group{ID: "m1", Kind: GroupMixer, Cols: 8}, Group{ID: "m2", Kind: GroupMixer, Cols: 8})
		},
		"плагин без действия":  func(l *Layout) { l.Groups[0].Tiles[0].Action = Action{Type: ActionPlugin, Plugin: "music"} },
		"действие без плагина": func(l *Layout) { l.Groups[0].Tiles[0].Action = Action{Type: ActionPlugin, PluginAction: "pause"} },
		"id плагина со слэшем": func(l *Layout) {
			l.Groups[0].Tiles[0].Action = Action{Type: ActionPlugin, Plugin: "a/b", PluginAction: "pause"}
		},
		"id действия в верхнем": func(l *Layout) {
			l.Groups[0].Tiles[0].Action = Action{Type: ActionPlugin, Plugin: "music", PluginAction: "Pause"}
		},
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
		`data-rev="3"`, `data-id="a"`, `data-id="big"`, ` data-confirm="1"`, `class="gn"><span>Main</span></div>`,
		`left:0.50em;width:5.00em`, `left:6.10em;width:1.00em`, `width:7.60em`,
		`left:0em;top:0.70em;width:2em;height:1em`, // 2×1 в левом верхнем углу под заголовком
		`left:3em;top:0.70em;width:2em;height:3em`, // крупная 2×3
		`left:0em;top:1.70em;width:3em;height:1em`, // 3×1 во втором ряду
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

func TestParseEmptyGroupHasNoNullTiles(t *testing.T) {
	l, err := Parse([]byte(`{"rev":1,"groups":[{"id":"g1","name":"A"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(l)
	if !strings.Contains(string(raw), `"tiles":[]`) {
		t.Fatalf("tiles должен быть [], а не null: %s", raw)
	}
}

func TestPluginActionTileIsValid(t *testing.T) {
	l := Layout{Groups: []Group{{ID: "g", Tiles: []Tile{
		{ID: "pause", W: 1, H: 1, Icon: "pause", Action: Action{Type: ActionPlugin, Plugin: "music", PluginAction: "toggle"}},
	}}}}
	if err := l.Validate(); err != nil {
		t.Fatal(err)
	}
	if got, _ := l.Tile("pause"); got.Action.Plugin != "music" || got.Action.PluginAction != "toggle" {
		t.Fatalf("%+v", got)
	}
	// из JSON и обратно: плагина может не быть в системе — раскладка всё равно читается
	raw := `{"rev":1,"groups":[{"id":"g","tiles":[{"id":"t","x":0,"y":0,"w":1,"h":1,"action":{"type":"plugin","plugin":"nobody","pluginAction":"x"}}]}]}`
	if _, err := Parse([]byte(raw)); err != nil {
		t.Fatal(err)
	}
}

func TestCSPForAllowsWebSocketOnOwnHost(t *testing.T) {
	got := CSPFor("192.168.1.5:7878")
	if !strings.Contains(got, "connect-src 'self' ws://192.168.1.5:7878 wss://192.168.1.5:7878;") {
		t.Fatalf("CSP: %s", got)
	}
	for _, bad := range []string{"", "a b", "x;script-src *", "evil.com'", strings.Repeat("a", 300)} {
		if CSPFor(bad) != CSP {
			t.Errorf("хост %q не должен попадать в CSP", bad)
		}
	}
	if !strings.Contains(clientJS, "/api/deck/ws") || !strings.Contains(clientJS, "/api/deck/state") {
		t.Error("клиент должен уметь и WebSocket, и опрос")
	}
}

func TestRenderPluginTileHasTextBox(t *testing.T) {
	l := valid()
	l.Groups[0].Tiles[0].Action = Action{Type: ActionPlugin, Plugin: "music", PluginAction: "now"}
	out, err := Render(ViewData{Layout: l})
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(out), "<s></s>"); n != 1 {
		t.Fatalf("блок текста только у плиток плагинов, нашли %d", n)
	}
}

func folder(id string, scenes ...string) Tile {
	return Tile{ID: id, W: 2, H: 3, Action: Action{Type: ActionFolder, Scenes: scenes}}
}

func TestFolderValidation(t *testing.T) {
	ok := func(scenes ...string) error {
		l := Layout{Groups: []Group{{ID: "g", Tiles: []Tile{folder("f", scenes...)}}}}
		return l.Validate()
	}
	if err := ok("A", "B"); err != nil {
		t.Fatal(err)
	}
	many := make([]string, 0, MaxFolderScenes+1)
	for i := 0; i <= MaxFolderScenes; i++ {
		many = append(many, string(rune('a'+i)))
	}
	if ok() == nil || ok(many...) == nil || ok("A", "A") == nil || ok("") == nil || ok(strings.Repeat("я", 201)) == nil {
		t.Fatal("плохие папки должны отклоняться")
	}
	if ok(many[:MaxFolderScenes]...) != nil {
		t.Fatal("максимум сцен допустим")
	}
}

func TestActionHasScene(t *testing.T) {
	f := Action{Type: ActionFolder, Scenes: []string{"A", "B"}}
	s := Action{Type: ActionScene, Scene: "A"}
	if !f.HasScene("B") || f.HasScene("C") || f.HasScene("") || !s.HasScene("A") || s.HasScene("B") || (Action{Type: ActionPlugin}).HasScene("") {
		t.Fatal("HasScene")
	}
}

func TestRenderFolderHasOverlayAndHighlight(t *testing.T) {
	l := Layout{Rev: 1, Groups: []Group{{ID: "g", Tiles: []Tile{folder("f", "Game", "Chat")}}}}
	out, err := Render(ViewData{Layout: l, Online: true, Scene: "Chat"})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{`data-folder="1"`, `data-for="f"`, `<div class="fs" data-scene="Game">`, `<div class="fs" data-scene="Chat">`, "t c-steel on"} {
		if !strings.Contains(s, want) {
			t.Errorf("нет %q", want)
		}
	}
}

func TestMuteTileIsValidAndHighlightedWhenMuted(t *testing.T) {
	l := Layout{Groups: []Group{{ID: "g", Tiles: []Tile{
		{ID: "mic", W: 1, H: 1, Icon: "mic", Action: Action{Type: ActionMute, Input: "Mic/Aux"}},
	}}}}
	if err := l.Validate(); err != nil {
		t.Fatal(err)
	}
	on, _ := Render(ViewData{Layout: l, Online: true, Muted: map[string]bool{"Mic/Aux": true}})
	off, _ := Render(ViewData{Layout: l, Online: true})
	if !strings.Contains(string(on), "c-steel on") || strings.Contains(string(off), "c-steel on") {
		t.Fatal("плитка mute подсвечена только пока источник заглушён")
	}
}

func TestMixerGroupWidthValidationAndRender(t *testing.T) {
	l := valid()
	l.Groups = append(l.Groups, Group{ID: "mix", Name: "Mixer", Kind: GroupMixer, Cols: 6, Tiles: []Tile{}})
	if err := l.Validate(); err != nil {
		t.Fatal(err)
	}
	if !l.HasMixer() || l.Groups[2].Width() != 6 {
		t.Fatalf("микшер и его ширина: %v %d", l.HasMixer(), l.Groups[2].Width())
	}
	plainL := valid()
	if plainL.HasMixer() {
		t.Fatal("в обычной раскладке микшера нет")
	}
	out, _ := Render(ViewData{Layout: l, Online: true})
	if strings.Count(string(out), `id="mx"`) != 1 {
		t.Fatal("страница должна содержать ровно одну область микшера")
	}
	plain, _ := Render(ViewData{Layout: plainL, Online: true})
	if strings.Contains(string(plain), `id="mx"`) {
		t.Fatal("без группы-микшера области нет")
	}
	// группа-микшер переживает разбор JSON
	back, err := Parse([]byte(`{"rev":1,"groups":[{"id":"m","kind":"mixer","cols":8,"tiles":[]}]}`))
	if err != nil || back.Groups[0].Cols != 8 {
		t.Fatalf("%v %+v", err, back)
	}
}
