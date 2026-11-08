// ocpack — упаковщик статики автономного плагина: дописывает в .wasm секцию oc.assets с содержимым папки.
//
//	ocpack pack <plugin.wasm> <папка> [-o результат.wasm]   упаковать (без -o файл заменяется)
//	ocpack list <plugin.wasm>                              показать, что внутри
//
// Это отдельная консольная утилита: приложение (трей, windowsgui) к ней отношения не имеет.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/compico/overlaycomposer/internal/wasmassets"
)

const usage = `usage:
  ocpack pack <plugin.wasm> <ui-dir> [-o out.wasm]
  ocpack list <plugin.wasm>`

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "ocpack:", err)
		os.Exit(1)
	}
}

func run(args []string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("%s", usage)
	}
	switch args[0] {
	case "pack":
		fs := flag.NewFlagSet("pack", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		dst := fs.String("o", "", "output file (default: replace the input)")
		// флаг разрешён и после позиционных аргументов: ocpack pack a.wasm ui -o b.wasm
		var pos []string
		rest := args[1:]
		for len(rest) > 0 {
			if err := fs.Parse(rest); err != nil {
				return fmt.Errorf("%v\n%s", err, usage)
			}
			rest = fs.Args()
			if len(rest) == 0 {
				break
			}
			pos = append(pos, rest[0])
			rest = rest[1:]
		}
		if len(pos) != 2 {
			return fmt.Errorf("%s", usage)
		}
		o := *dst
		if o == "" {
			o = pos[0]
		}
		if err := wasmassets.PackFile(pos[0], pos[1], o); err != nil {
			return err
		}
		return list(o, out)
	case "list":
		if len(args) != 2 {
			return fmt.Errorf("%s", usage)
		}
		return list(args[1], out)
	}
	return fmt.Errorf("unknown command %q\n%s", args[0], usage)
}

func list(file string, out io.Writer) error {
	b, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	a, err := wasmassets.Of(b)
	if err != nil {
		return err
	}
	if a.Len() == 0 {
		fmt.Fprintf(out, "%s: no %s section\n", file, wasmassets.Section)
		return nil
	}
	fmt.Fprintf(out, "%s: %d files, %d bytes\n", file, a.Len(), a.Size())
	for _, p := range a.Paths() {
		data, mime, _ := a.Get(p)
		fmt.Fprintf(out, "  %-40s %8d  %s\n", p, len(data), mime)
	}
	return nil
}
