package wasmassets

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func makeZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for n, c := range files {
		w, err := zw.Create(n)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(c))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestSectionRoundTripAndStrip(t *testing.T) {
	for _, size := range []int{0, 1, 127, 128, 16383, 16384, 300000} { // границы LEB128
		data := bytes.Repeat([]byte{0xAB}, size)
		w, err := AppendSection(wasmMagic, "other", []byte("keep"))
		if err != nil {
			t.Fatal(err)
		}
		w, err = AppendSection(w, Section, data)
		if err != nil {
			t.Fatal(err)
		}
		got, ok, err := ReadSection(w, Section)
		if err != nil || !ok || !bytes.Equal(got, data) {
			t.Fatalf("size %d: ok=%v err=%v len=%d", size, ok, err, len(got))
		}
		stripped, err := StripSection(w, Section)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok, _ := ReadSection(stripped, Section); ok {
			t.Fatal("section must be gone")
		}
		if k, ok, _ := ReadSection(stripped, "other"); !ok || string(k) != "keep" {
			t.Fatal("other sections must stay")
		}
	}
	if _, ok, err := ReadSection(wasmMagic, Section); ok || err != nil {
		t.Fatal("no section is not an error")
	}
}

func TestSectionBadModules(t *testing.T) {
	good, _ := AppendSection(wasmMagic, "x", []byte("data"))
	cases := map[string][]byte{
		"empty":        nil,
		"bad header":   []byte("\x00asm\x02\x00\x00\x00"),
		"text":         []byte("hello world"),
		"truncated":    good[:len(good)-2],
		"huge size":    append(append([]byte{}, wasmMagic...), 0x00, 0xff, 0xff, 0xff, 0xff, 0x0f),
		"endless leb":  append(append([]byte{}, wasmMagic...), 0x00, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80),
		"bad name len": append(append([]byte{}, wasmMagic...), 0x00, 0x02, 0x7f, 0x00),
	}
	for n, w := range cases {
		if _, _, err := ReadSection(w, "x"); err == nil {
			t.Errorf("%s: want error", n)
		}
		if _, err := AppendSection(w, "x", nil); err == nil {
			t.Errorf("%s: append must refuse a broken module", n)
		}
	}
}

func TestParseAssetsOK(t *testing.T) {
	a, err := Parse(makeZip(t, map[string]string{"index.html": "<p>hi", "js/app.js": "1", "img/a.PNG": "x"}))
	if err != nil {
		t.Fatal(err)
	}
	if a.Len() != 3 || a.Size() != len("<p>hi")+2 || strings.Join(a.Paths(), ",") != "img/a.PNG,index.html,js/app.js" {
		t.Fatalf("%v %d", a.Paths(), a.Size())
	}
	b, mime, ok := a.Get("index.html")
	if !ok || string(b) != "<p>hi" || !strings.HasPrefix(mime, "text/html") {
		t.Fatal(string(b), mime, ok)
	}
	if _, m, _ := a.Get("img/a.PNG"); m != "image/png" {
		t.Fatalf("extension is case-insensitive: %q", m)
	}
	if _, _, ok := a.Get("nope.html"); ok || (*Assets)(nil).Has("x") || (*Assets)(nil).Len() != 0 {
		t.Fatal("missing file / nil assets")
	}
}

func TestParseAssetsRejects(t *testing.T) {
	bad := map[string]map[string]string{
		"traversal":     {"../evil.js": "x"},
		"inner dotdot":  {"a/../b.js": "x"},
		"absolute":      {"/etc/x.js": "x"},
		"backslash":     {`a\b.js`: "x"},
		"hidden file":   {".env.json": "x"},
		"hidden dir":    {".git/x.js": "x"},
		"bad extension": {"run.exe": "x"},
		"no extension":  {"README": "x"},
		"space":         {"a b.js": "x"},
		"case clash":    {"A.js": "1", "a.js": "2"},
		"big file":      {"big.js": strings.Repeat("a", MaxFile+1)},
	}
	for n, files := range bad {
		if _, err := Parse(makeZip(t, files)); err == nil {
			t.Errorf("%s: want error", n)
		}
	}
	if _, err := Parse([]byte("not a zip")); err == nil {
		t.Error("garbage")
	}
	if _, err := Parse(nil); err == nil {
		t.Error("empty")
	}
}

func TestParseAssetsLimits(t *testing.T) {
	many := map[string]string{}
	for i := 0; i <= MaxFiles; i++ {
		many[strings.Repeat("a", 3)+string(rune('a'+i%26))+strings.Repeat("b", i/26)+".js"] = "x"
	}
	if _, err := Parse(makeZip(t, many)); err == nil {
		t.Error("too many files")
	}
	// каждый файл в пределах, сумма — нет (нули сжимаются, поэтому сам архив мал: проверяется именно распакованный размер)
	total := map[string]string{}
	for i := 0; i < 5; i++ {
		total[string(rune('a'+i))+".js"] = strings.Repeat("0", MaxFile)
	}
	if _, err := Parse(makeZip(t, total)); err == nil || !strings.Contains(err.Error(), "total") {
		t.Errorf("total size: %v", err)
	}
}

// Размер в заголовке zip подделан (мал), реальный — огромный: распаковка всё равно упирается в лимит.
func TestParseAssetsForgedSize(t *testing.T) {
	z := makeZip(t, map[string]string{"a.txt": strings.Repeat("0", 3<<20)})
	i := bytes.Index(z, []byte{'P', 'K', 0x01, 0x02}) // central directory
	if i < 0 {
		t.Fatal("no central directory")
	}
	binary.LittleEndian.PutUint32(z[i+24:], 10) // uncompressed size
	if _, err := Parse(z); err == nil {
		t.Fatal("forged size must not let a bomb through")
	}
}

func TestPackDir(t *testing.T) {
	dir := t.TempDir()
	must := func(p, c string) {
		t.Helper()
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must("index.html", "<h1>x")
	must("js/app.js", "1")
	must(".DS_Store", "junk")
	must(".git/config", "junk")
	z1, err := PackDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	z2, _ := PackDir(dir)
	if !bytes.Equal(z1, z2) {
		t.Fatal("packing must be deterministic")
	}
	a, err := Parse(z1)
	if err != nil || strings.Join(a.Paths(), ",") != "index.html,js/app.js" {
		t.Fatalf("%v %v", a.Paths(), err)
	}

	must("tool.exe", "x")
	if _, err := PackDir(dir); err == nil || !strings.Contains(err.Error(), "tool.exe") {
		t.Fatalf("bad extension must name the file: %v", err)
	}
	_ = os.Remove(filepath.Join(dir, "tool.exe"))
	if err := os.Symlink("/etc/passwd", filepath.Join(dir, "link.txt")); err == nil {
		if _, err := PackDir(dir); err == nil {
			t.Fatal("symlink must be refused")
		}
	}
	if _, err := PackDir(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("missing dir")
	}
}

func TestPackWasmIsIdempotentAndKeepsCode(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "index.html"), []byte("v1"), 0o644)
	base, _ := AppendSection(wasmMagic, "code", []byte("CODE"))
	w1, err := PackWasm(base, dir)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, "index.html"), []byte("v2"), 0o644)
	w2, err := PackWasm(w1, dir) // повторная упаковка заменяет секцию
	if err != nil {
		t.Fatal(err)
	}
	secs, _ := wasmSections(w2)
	n := 0
	for _, s := range secs {
		if s.name == Section {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("exactly one %s section expected, got %d", Section, n)
	}
	if c, ok, _ := ReadSection(w2, "code"); !ok || string(c) != "CODE" {
		t.Fatal("code sections must stay")
	}
	a, err := Of(w2)
	if err != nil {
		t.Fatal(err)
	}
	if b, _, _ := a.Get("index.html"); string(b) != "v2" {
		t.Fatalf("new content expected: %q", b)
	}
	if _, err := PackWasm([]byte("nope"), dir); err == nil {
		t.Fatal("not a wasm")
	}
	if a, err := Of([]byte("plain text")); a != nil || err != nil {
		t.Fatal("non-module has no assets and is rejected elsewhere")
	}
}
