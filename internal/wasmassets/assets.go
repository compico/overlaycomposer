// Package wasmassets — статика автономных плагинов: zip внутри custom section "oc.assets" их .wasm-файла.
// Пакет не зависит от рантайма wasm, поэтому им пользуется и утилита cmd/ocpack.
package wasmassets

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Статика плагина (HTML виджета, js, css, картинки) лежит внутри самого .wasm: в custom section "oc.assets" хранится zip.
// Так плагин остаётся одним файлом, sha256 релиза покрывает и код, и интерфейс, а секция дописывается к уже собранному
// модулю на любом языке без перекомпиляции. Хост читает её при сканировании (плагин для этого не запускается)
// и отдаёт сам, из памяти — см. server: /plugins/<id>/ui/*.

// Section — имя custom section с zip-архивом статики.
const Section = "oc.assets"

// MaxWasmBytes — предел размера .wasm-файла для хоста.
const MaxWasmBytes = 64 << 20

// Лимиты статики: плагин — чужой код, а архив распаковывается в память хоста.
const (
	MaxFiles = 256
	MaxFile  = 2 << 20
	MaxSize  = 8 << 20
	maxPath  = 200
)

var assetSegRe = regexp.MustCompile(`^[A-Za-z0-9._~@-]+$`)

// assetTypes — закрытый список расширений и типов содержимого. Всё остальное в архив не допускается.
var assetTypes = map[string]string{
	".html":  "text/html; charset=utf-8",
	".htm":   "text/html; charset=utf-8",
	".js":    "text/javascript; charset=utf-8",
	".mjs":   "text/javascript; charset=utf-8",
	".css":   "text/css; charset=utf-8",
	".json":  "application/json; charset=utf-8",
	".txt":   "text/plain; charset=utf-8",
	".png":   "image/png",
	".jpg":   "image/jpeg",
	".jpeg":  "image/jpeg",
	".gif":   "image/gif",
	".webp":  "image/webp",
	".svg":   "image/svg+xml",
	".ico":   "image/x-icon",
	".woff2": "font/woff2",
	".woff":  "font/woff",
	".mp3":   "audio/mpeg",
	".ogg":   "audio/ogg",
	".wav":   "audio/wav",
}

// ValidPath проверяет путь файла внутри архива: относительный, с «/», без «..», скрытых файлов и неизвестных расширений.
func ValidPath(p string) error {
	if p == "" || len(p) > maxPath {
		return fmt.Errorf("path must be 1..%d characters", maxPath)
	}
	if strings.HasPrefix(p, "/") || strings.HasSuffix(p, "/") {
		return errors.New("path must be relative and name a file")
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "." || seg == ".." || strings.HasPrefix(seg, ".") || !assetSegRe.MatchString(seg) {
			return fmt.Errorf("bad path segment %q (allowed: letters, digits, . _ ~ @ -; no hidden files)", seg)
		}
	}
	if _, ok := assetTypes[strings.ToLower(path.Ext(p))]; !ok {
		return fmt.Errorf("file type %q is not allowed", path.Ext(p))
	}
	return nil
}

type asset struct {
	data []byte
	mime string
}

// Assets — распакованная и проверенная статика плагина. Только для чтения.
type Assets struct {
	files map[string]asset
	size  int
}

// Get возвращает содержимое и тип файла.
func (a *Assets) Get(p string) (data []byte, mime string, ok bool) {
	if a == nil {
		return nil, "", false
	}
	f, ok := a.files[p]
	return f.data, f.mime, ok
}

// Has — есть ли такой файл.
func (a *Assets) Has(p string) bool { _, _, ok := a.Get(p); return ok }

// Len — число файлов; Size — их общий размер.
func (a *Assets) Len() int {
	if a == nil {
		return 0
	}
	return len(a.files)
}
func (a *Assets) Size() int {
	if a == nil {
		return 0
	}
	return a.size
}

// Paths — пути файлов по возрастанию.
func (a *Assets) Paths() []string {
	if a == nil {
		return nil
	}
	out := make([]string, 0, len(a.files))
	for p := range a.files {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// Parse проверяет и распаковывает zip. Любое нарушение — ошибка: плагин с кривой статикой не загружается.
func Parse(zipData []byte) (*Assets, error) {
	if len(zipData) > MaxSize {
		return nil, fmt.Errorf("assets archive is larger than %d MB", MaxSize>>20)
	}
	zr, err := zip.NewReader(bytes.NewReader(zipData), int64(len(zipData)))
	if err != nil {
		return nil, fmt.Errorf("assets: not a zip archive: %w", err)
	}
	if len(zr.File) > MaxFiles*2 { // с запасом на записи каталогов
		return nil, fmt.Errorf("assets: too many entries")
	}
	a := &Assets{files: map[string]asset{}}
	lower := map[string]string{}
	var declared uint64
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		if !f.Mode().IsRegular() {
			return nil, fmt.Errorf("assets: %q is not a regular file", f.Name)
		}
		if f.Flags&0x1 != 0 {
			return nil, fmt.Errorf("assets: %q is encrypted", f.Name)
		}
		if err := ValidPath(f.Name); err != nil {
			return nil, fmt.Errorf("assets: %q: %w", f.Name, err)
		}
		if _, dup := a.files[f.Name]; dup {
			return nil, fmt.Errorf("assets: duplicate file %q", f.Name)
		}
		if other, dup := lower[strings.ToLower(f.Name)]; dup {
			return nil, fmt.Errorf("assets: %q and %q differ only in letter case", other, f.Name)
		}
		if len(a.files) >= MaxFiles {
			return nil, fmt.Errorf("assets: more than %d files", MaxFiles)
		}
		if f.UncompressedSize64 > MaxFile {
			return nil, fmt.Errorf("assets: %q is larger than %d MB", f.Name, MaxFile>>20)
		}
		declared += f.UncompressedSize64
		if declared > MaxSize {
			return nil, fmt.Errorf("assets: total size is larger than %d MB", MaxSize>>20)
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("assets: %q: %w", f.Name, err)
		}
		// Размер в заголовке zip можно подделать: читаем с ограничением и сверяем фактический.
		b, err := io.ReadAll(io.LimitReader(rc, MaxFile+1))
		rc.Close()
		if err != nil {
			return nil, fmt.Errorf("assets: %q: %w", f.Name, err)
		}
		if len(b) > MaxFile {
			return nil, fmt.Errorf("assets: %q is larger than %d MB", f.Name, MaxFile>>20)
		}
		a.size += len(b)
		if a.size > MaxSize {
			return nil, fmt.Errorf("assets: total size is larger than %d MB", MaxSize>>20)
		}
		lower[strings.ToLower(f.Name)] = f.Name
		a.files[f.Name] = asset{data: b, mime: assetTypes[strings.ToLower(path.Ext(f.Name))]}
	}
	return a, nil
}

// PackDir упаковывает папку в zip для секции oc.assets. Результат детерминирован (порядок и даты фиксированы),
// скрытые файлы и каталоги пропускаются, остальное проверяется теми же правилами, что и при загрузке плагина.
func PackDir(dir string) ([]byte, error) {
	root := os.DirFS(dir)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	err := fs.WalkDir(root, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == "." {
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("%s: only regular files are allowed (no symlinks)", p)
		}
		if err := ValidPath(p); err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		b, err := fs.ReadFile(root, p)
		if err != nil {
			return err
		}
		w, err := zw.CreateHeader(&zip.FileHeader{Name: p, Method: zip.Deflate, Modified: time.Unix(0, 0).UTC()})
		if err != nil {
			return err
		}
		_, err = w.Write(b)
		return err
	})
	if err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	if _, err := Parse(buf.Bytes()); err != nil { // те же лимиты, что и у хоста
		return nil, err
	}
	return buf.Bytes(), nil
}

// PackWasm возвращает wasm с секцией oc.assets из папки dir (прежняя секция заменяется: упаковка идемпотентна).
func PackWasm(wasm []byte, dir string) ([]byte, error) {
	zipData, err := PackDir(dir)
	if err != nil {
		return nil, err
	}
	base, err := StripSection(wasm, Section)
	if err != nil {
		return nil, err
	}
	out, err := AppendSection(base, Section, zipData)
	if err != nil {
		return nil, err
	}
	if len(out) > MaxWasmBytes {
		return nil, fmt.Errorf("result is larger than %d MB (host limit)", MaxWasmBytes>>20)
	}
	return out, nil
}

// PackFile — PackWasm над файлами: читает in, пишет out (может совпадать с in).
func PackFile(in, dir, out string) error {
	wasm, err := os.ReadFile(in)
	if err != nil {
		return err
	}
	res, err := PackWasm(wasm, dir)
	if err != nil {
		return err
	}
	tmp := out + ".tmp"
	if err := os.WriteFile(tmp, res, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Clean(out)); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// ---- custom sections wasm ----

var wasmMagic = []byte{0x00, 'a', 's', 'm', 0x01, 0x00, 0x00, 0x00}

func uleb(v uint64) []byte {
	var b []byte
	for {
		c := byte(v & 0x7f)
		v >>= 7
		if v != 0 {
			b = append(b, c|0x80)
			continue
		}
		return append(b, c)
	}
}

func readULEB(b []byte) (v uint64, n int, err error) {
	var shift uint
	for i, c := range b {
		if shift > 35 {
			return 0, 0, errors.New("wasm: LEB128 is too long")
		}
		v |= uint64(c&0x7f) << shift
		if c&0x80 == 0 {
			return v, i + 1, nil
		}
		shift += 7
	}
	return 0, 0, errors.New("wasm: truncated LEB128")
}

type wasmSection struct {
	id         byte
	start, end int // границы всей секции (с заголовком) в файле
	name       string
	data       []byte // для custom — содержимое после имени
}

func wasmSections(wasm []byte) ([]wasmSection, error) {
	if len(wasm) < len(wasmMagic) || !bytes.Equal(wasm[:len(wasmMagic)], wasmMagic) {
		return nil, errors.New("not a wasm module (bad header)")
	}
	var out []wasmSection
	for i := len(wasmMagic); i < len(wasm); {
		s := wasmSection{id: wasm[i], start: i}
		size, n, err := readULEB(wasm[i+1:])
		if err != nil {
			return nil, err
		}
		body := i + 1 + n
		if size > uint64(len(wasm)-body) {
			return nil, errors.New("wasm: section goes past end of file")
		}
		s.end = body + int(size)
		if s.id == 0 {
			nl, m, err := readULEB(wasm[body:s.end])
			if err != nil || nl > uint64(s.end-body-m) {
				return nil, errors.New("wasm: bad custom section name")
			}
			s.name = string(wasm[body+m : body+m+int(nl)])
			s.data = wasm[body+m+int(nl) : s.end]
		}
		out = append(out, s)
		i = s.end
	}
	return out, nil
}

// ReadSection возвращает содержимое custom section с таким именем (первой из найденных).
func ReadSection(wasm []byte, name string) ([]byte, bool, error) {
	secs, err := wasmSections(wasm)
	if err != nil {
		return nil, false, err
	}
	for _, s := range secs {
		if s.id == 0 && s.name == name {
			return s.data, true, nil
		}
	}
	return nil, false, nil
}

// AppendSection дописывает custom section в конец модуля.
func AppendSection(wasm []byte, name string, data []byte) ([]byte, error) {
	if _, err := wasmSections(wasm); err != nil {
		return nil, err
	}
	body := append(uleb(uint64(len(name))), name...)
	body = append(body, data...)
	out := make([]byte, 0, len(wasm)+len(body)+8)
	out = append(out, wasm...)
	out = append(out, 0)
	out = append(out, uleb(uint64(len(body)))...)
	return append(out, body...), nil
}

// StripSection убирает все custom section с таким именем.
func StripSection(wasm []byte, name string) ([]byte, error) {
	secs, err := wasmSections(wasm)
	if err != nil {
		return nil, err
	}
	out := append([]byte(nil), wasm[:len(wasmMagic)]...)
	for _, s := range secs {
		if s.id == 0 && s.name == name {
			continue
		}
		out = append(out, wasm[s.start:s.end]...)
	}
	return out, nil
}

// Of достаёт статику из wasm-файла; нет секции — nil без ошибки.
func Of(wasm []byte) (*Assets, error) {
	if !bytes.HasPrefix(wasm, wasmMagic) {
		return nil, nil // не модуль: такой файл отклоняет Compile ещё до этого места
	}
	z, ok, err := ReadSection(wasm, Section)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	return Parse(z)
}
