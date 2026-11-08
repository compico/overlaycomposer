package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPackAndList(t *testing.T) {
	tmp := t.TempDir()
	ui := filepath.Join(tmp, "ui")
	_ = os.MkdirAll(ui, 0o755)
	_ = os.WriteFile(filepath.Join(ui, "index.html"), []byte("<p>x"), 0o644)
	wasm := filepath.Join(tmp, "p.wasm")
	_ = os.WriteFile(wasm, []byte("\x00asm\x01\x00\x00\x00"), 0o644)
	out := filepath.Join(tmp, "out.wasm")

	var buf bytes.Buffer
	if err := run([]string{"pack", wasm, ui, "-o", out}, &buf); err != nil { // флаг после аргументов
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "index.html") || !strings.Contains(buf.String(), "1 files") {
		t.Fatalf("output: %s", buf.String())
	}
	if b, _ := os.ReadFile(wasm); len(b) != 8 {
		t.Fatal("the input must stay untouched when -o is given")
	}
	buf.Reset()
	if err := run([]string{"pack", out, ui}, &buf); err != nil { // на месте, повторно
		t.Fatal(err)
	}
	buf.Reset()
	if err := run([]string{"list", out}, &buf); err != nil || !strings.Contains(buf.String(), "text/html") {
		t.Fatalf("list: %v %s", err, buf.String())
	}
	buf.Reset()
	if err := run([]string{"list", wasm}, &buf); err != nil || !strings.Contains(buf.String(), "no oc.assets") {
		t.Fatalf("list without section: %v %s", err, buf.String())
	}
}

func TestBadUsageAndInput(t *testing.T) {
	tmp := t.TempDir()
	notWasm := filepath.Join(tmp, "x.wasm")
	_ = os.WriteFile(notWasm, []byte("hello"), 0o644)
	ui := t.TempDir()
	_ = os.WriteFile(filepath.Join(ui, "run.exe"), []byte("x"), 0o644)
	for name, args := range map[string][]string{
		"no args":      {},
		"unknown":      {"zip"},
		"pack short":   {"pack", notWasm},
		"list long":    {"list", "a", "b"},
		"not wasm":     {"pack", notWasm, ui},
		"missing file": {"list", filepath.Join(tmp, "nope.wasm")},
		"bad flag":     {"pack", "-z", notWasm, ui},
	} {
		if err := run(args, &bytes.Buffer{}); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
	good := filepath.Join(tmp, "g.wasm")
	_ = os.WriteFile(good, []byte("\x00asm\x01\x00\x00\x00"), 0o644)
	if err := run([]string{"pack", good, ui}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "run.exe") {
		t.Fatalf("a forbidden file type must be named: %v", err)
	}
	if b, _ := os.ReadFile(good); len(b) != 8 {
		t.Fatal("a failed pack must not change the file")
	}
}
