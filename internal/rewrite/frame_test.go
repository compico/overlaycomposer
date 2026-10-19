package rewrite

import "testing"

func TestNeutralize(t *testing.T) {
	cases := [][2]string{
		// код из DonationAlerts
		{"try { return window.self !== window.top; } catch { return true; }", "try { return (false); } catch { return true; }"},
		{"window.top !== window.self", "(false)"},
		{"window.self != window.top", "(false)"},
		{"if (top != self) top.location = self.location", "if ((false)) top.location = self.location"},
		{"if (self !== top) {}", "if ((false)) {}"},
		{"var a=top!==self;", "var a=(false);"},
		{"window !== window.top", "(false)"},
		{"window.top !== window", "(false)"},
		{"window.parent !== window", "(false)"},
		{"window !== window.parent", "(false)"},
		{"window.parent != window.self", "(false)"},
		{"top.location != self.location", "(false)"},
		{"window.top.location.href !== window.location.href", "(false)"},
		{"window.location !== window.top.location", "(false)"},
		// обфусцированный код DonationAlerts
		{"try{return window['self']!==window[_0x40f318(0xa5)];}catch(e){return!![];}", "try{return (false);}catch(e){return!![];}"},
		{"window[_0x1(0x2)]!==window['self']", "(false)"},
		{"window[\"self\"] != window[\"top\"]", "(false)"},
		// обратные проверки — «я на верхнем уровне?»
		{"if (window.self === window.top) init()", "if ((true)) init()"},
		{"window.top == window", "(true)"},
	}
	for _, c := range cases {
		got, n := NeutralizeFrameChecks(c[0])
		if got != c[1] || n != 1 {
			t.Errorf("вход %q\n  ждали %q (1)\n  got   %q (%d)", c[0], c[1], got, n)
		}
	}
}

func TestUntouched(t *testing.T) {
	for _, src := range []string{
		"other.self !== other.top",
		"obj.window !== obj.top",
		"selfish !== top2",
		"self.location !== top.foo",
		"top !== self.location",
		"var parent = el.parentNode; if (parent !== window) {}",
		"if (parent !== self) {}",
		"window.topLevel !== window.self",
		"rect.top !== rect.self",
		"const x = window.top;",
		"window.self === 5",
	} {
		got, n := NeutralizeFrameChecks(src)
		if got != src || n != 0 {
			t.Errorf("изменён %q → %q (%d)", src, got, n)
		}
	}
}

func TestSeveral(t *testing.T) {
	got, n := NeutralizeFrameChecks("a = window.self !== window.top; b = top != self; c = window.top.location != window.location;")
	if got != "a = (false); b = (false); c = (false);" || n != 3 {
		t.Errorf("%q %d", got, n)
	}
	// два подряд через запятую
	got, n = NeutralizeFrameChecks("x(top!==self,top!==self)")
	if got != "x((false),(false))" || n != 2 {
		t.Errorf("%q %d", got, n)
	}
}
