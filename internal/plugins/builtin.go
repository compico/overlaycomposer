package plugins

import (
	"embed"
	"io/fs"
)

//go:embed all:builtin
var builtinFS embed.FS

// Builtin — плагины, встроенные в бинарник (builtin/<id>/plugin.json + файлы виджета).
func Builtin() fs.FS {
	sub, err := fs.Sub(builtinFS, "builtin")
	if err != nil {
		panic(err) // каталог встроен на этапе сборки
	}
	return sub
}
