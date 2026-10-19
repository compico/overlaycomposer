// Package web — встроенные в бинарник страницы и собранные бандлы клиента.
// Каталог dist/ заполняет `go run ./cmd/build client` (esbuild) — без этого шага бинарник соберётся, но редактор будет пустым.
package web

import (
	"embed"
	"io/fs"
)

//go:embed client/editor/index.html client/runtime/index.html all:dist
var files embed.FS

func read(name string) ([]byte, error) { return files.ReadFile(name) }

func EditorHTML() ([]byte, error)  { return read("client/editor/index.html") }
func OverlayHTML() ([]byte, error) { return read("client/runtime/index.html") }

// Dist — файлы бандла (runtime.js, editor.js, *.map).
func Dist() fs.FS {
	sub, err := fs.Sub(files, "dist")
	if err != nil {
		panic(err) // каталог точно есть: он встроен
	}
	return sub
}
