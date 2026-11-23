package chat

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseCommand(t *testing.T) {
	for in, want := range map[string]struct {
		name string
		args []string
	}{
		"!np":             {"np", []string{}},
		"  !NP  ":         {"np", []string{}},
		"!info @compico":  {"info", []string{"@compico"}},
		"!Info  A   B\tC": {"info", []string{"A", "B", "C"}},
		"!cmd_1 x":        {"cmd_1", []string{"x"}},
	} {
		name, args, ok := ParseCommand(in)
		if !ok || name != want.name || !reflect.DeepEqual(args, want.args) {
			t.Errorf("%q: %q %q %v", in, name, args, ok)
		}
	}
	for _, bad := range []string{"", "!", "! np", "!!np", "!!!", "np", " np !np", "!привет", "!np!", "!a-b", "!" + strings.Repeat("a", 33), "hello !np", "!\tnp"} {
		if name, _, ok := ParseCommand(bad); ok {
			t.Errorf("%q разобрано как команда %q", bad, name)
		}
	}
}

func TestParseCommandLimits(t *testing.T) {
	_, args, ok := ParseCommand("!x " + strings.Repeat("a ", 40) + strings.Repeat("b", 500))
	if !ok || len(args) != maxCommandArgs {
		t.Fatalf("аргументов %d", len(args))
	}
	_, args, _ = ParseCommand("!x " + strings.Repeat("b", 500))
	if len(args) != 1 || len(args[0]) != maxCommandArg {
		t.Fatalf("длина аргумента %d", len(args[0]))
	}
	if _, _, ok := ParseCommand("!" + strings.Repeat("a", 32)); !ok {
		t.Fatal("имя из 32 символов допустимо")
	}
}
