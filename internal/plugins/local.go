package plugins

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/compico/overlaycomposer/internal/model"
)

// Пределы для «виджета из файла»: страница с парой скриптов и картинок, а не произвольная выгрузка на диск.
const (
	MaxLocalFiles = 64
	MaxLocalBytes = 6 << 20 // в сумме; меньше BodyLimit HTTP-сервера
	maxLocalDepth = 4
	maxLocalName  = 64
)

// LocalFile — один загруженный файл: Path относительный, с «/» (подпапки разрешены).
type LocalFile struct {
	Path string
	Data []byte
}

// localEntry выбирает точку входа: index.html, иначе единственный .html в корне.
func localEntry(files []LocalFile) (string, error) {
	var roots []string
	for _, f := range files {
		if strings.Contains(f.Path, "/") {
			continue
		}
		ext := strings.ToLower(path.Ext(f.Path))
		if f.Path == "index.html" {
			return f.Path, nil
		}
		if ext == ".html" || ext == ".htm" {
			roots = append(roots, f.Path)
		}
	}
	switch len(roots) {
	case 0:
		return "", errors.New("no HTML file in the upload")
	case 1:
		return roots[0], nil
	}
	return "", errors.New("several HTML files: name the main one index.html")
}

func cleanLocalPath(p string) (string, error) {
	p = strings.ReplaceAll(p, "\\", "/")
	if p == "" || !fs.ValidPath(p) || strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("invalid file path %q", p)
	}
	parts := strings.Split(p, "/")
	if len(parts) > maxLocalDepth {
		return "", fmt.Errorf("path too deep: %q", p)
	}
	for _, seg := range parts {
		if strings.HasPrefix(seg, ".") { // .git, .env и скрытые временные папки загрузчика
			return "", fmt.Errorf("hidden path is not allowed: %q", p)
		}
	}
	if p == ManifestFile {
		return "", fmt.Errorf("%s is created by the app: rename the file", ManifestFile)
	}
	return p, nil
}

// InstallLocal сохраняет загруженный HTML (с файлами рядом) как плагин в dir/<id>/ с минимальным манифестом
// без разрешений и настроек. Запись идёт во временную папку и переименовывается — недописанный виджет не виден.
// Возвращает готовый манифест; реестр нужно пересканировать.
func InstallLocal(dir string, reg *Registry, name string, files []LocalFile) (Manifest, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > maxLocalName {
		return Manifest{}, fmt.Errorf("name: 1..%d characters", maxLocalName)
	}
	if len(files) == 0 || len(files) > MaxLocalFiles {
		return Manifest{}, fmt.Errorf("files: 1..%d", MaxLocalFiles)
	}
	seen := map[string]bool{}
	total := 0
	clean := make([]LocalFile, 0, len(files))
	for _, f := range files {
		p, err := cleanLocalPath(f.Path)
		if err != nil {
			return Manifest{}, err
		}
		if seen[p] {
			return Manifest{}, fmt.Errorf("duplicate file %q", p)
		}
		seen[p] = true
		total += len(f.Data)
		clean = append(clean, LocalFile{Path: p, Data: f.Data})
	}
	if total > MaxLocalBytes {
		return Manifest{}, fmt.Errorf("upload is too large (max %d MB)", MaxLocalBytes>>20)
	}
	entry, err := localEntry(clean)
	if err != nil {
		return Manifest{}, err
	}

	// Свободный id: занятые встроенные/установленные плагины и папки на диске не трогаем.
	reg.Scan() // id у плагина может не совпадать с именем папки: сверяем с тем, что реально загружено
	base := "local-" + model.Slugify(name)
	if len(base) > 60 {
		base = strings.TrimRight(base[:60], "-")
	}
	id := base
	for n := 2; ; n++ {
		_, taken := reg.find(id)
		if !taken {
			if _, err := os.Stat(filepath.Join(dir, id)); errors.Is(err, fs.ErrNotExist) {
				break
			}
		}
		id = fmt.Sprintf("%s-%d", base, n)
		if n > 999 {
			return Manifest{}, errors.New("too many widgets with this name")
		}
	}

	m := Manifest{ID: id, Name: name, Description: "Local HTML widget", Entry: entry, DefaultSize: Size{Width: 640, Height: 360}}
	if err := m.Validate(); err != nil {
		return Manifest{}, err
	}
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return Manifest{}, err
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Manifest{}, err
	}
	tmp, err := os.MkdirTemp(dir, ".install-local-")
	if err != nil {
		return Manifest{}, err
	}
	defer os.RemoveAll(tmp) // после успешного Rename папки уже нет — вызов безвреден
	for _, f := range clean {
		dst := filepath.Join(tmp, filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return Manifest{}, err
		}
		if err := os.WriteFile(dst, f.Data, 0o644); err != nil {
			return Manifest{}, err
		}
	}
	if err := os.WriteFile(filepath.Join(tmp, ManifestFile), raw, 0o644); err != nil {
		return Manifest{}, err
	}
	if err := os.Rename(tmp, filepath.Join(dir, id)); err != nil {
		return Manifest{}, err
	}
	return m, nil
}
