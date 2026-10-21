// Сборщик проекта (вместо Makefile-магии): go run ./cmd/build <команда>
//
//	client             бандлы клиента (esbuild) → web/dist
//	bin [windows|linux|all]   client + кросс-компиляция бинарников → build/
//	clean              удалить web/dist (кроме .gitkeep) и build/
//
// Все бинарники собираются с CGO_ENABLED=0, поэтому кросс-компиляция работает с любой ОС.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/evanw/esbuild/pkg/api"
)

const rsrcVersion = "v0.10.2"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	root, err := projectRoot()
	if err != nil {
		fail(err)
	}
	switch os.Args[1] {
	case "client":
		fail(buildClient(root, hasFlag("--dev")))
	case "bin":
		target := "all"
		for _, a := range os.Args[2:] {
			if !strings.HasPrefix(a, "-") {
				target = a
			}
		}
		fail(buildBin(root, target))
	case "clean":
		fail(clean(root))
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: go run ./cmd/build client [--dev] | bin [windows|linux|all] | clean")
}

func hasFlag(f string) bool {
	for _, a := range os.Args[2:] {
		if a == f {
			return true
		}
	}
	return false
}

func fail(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "ошибка:", err)
		os.Exit(1)
	}
}

// projectRoot — каталог с go.mod (запуск из корня проекта).
func projectRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			return d, nil
		}
		if filepath.Dir(d) == d {
			return "", fmt.Errorf("go.mod не найден выше %s", dir)
		}
	}
}

func run(dir string, env []string, name string, args ...string) error {
	fmt.Printf("$ %s %s\n", name, strings.Join(args, " "))
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

// ---- клиент ----

func ensureNodeModules(root string) error {
	web := filepath.Join(root, "web")
	if _, err := os.Stat(filepath.Join(web, "node_modules", "three", "package.json")); err == nil {
		return nil
	}
	npm := "npm"
	if runtime.GOOS == "windows" {
		npm = "npm.cmd"
	}
	if _, err := exec.LookPath(npm); err != nil {
		return fmt.Errorf("нужен npm (Node.js), чтобы поставить three.js: %w", err)
	}
	return run(web, nil, npm, "install", "--no-audit", "--no-fund")
}

func buildClient(root string, dev bool) error {
	if err := ensureNodeModules(root); err != nil {
		return err
	}
	dist := filepath.Join(root, "web", "dist")
	if err := os.MkdirAll(dist, 0o755); err != nil {
		return err
	}
	opts := api.BuildOptions{
		AbsWorkingDir: filepath.Join(root, "web"),
		EntryPointsAdvanced: []api.EntryPoint{
			{OutputPath: "runtime", InputPath: "client/runtime/main.ts"},
			{OutputPath: "editor", InputPath: "client/editor/main.ts"},
		},
		Outdir:    dist,
		Bundle:    true,
		Format:    api.FormatESModule,
		Platform:  api.PlatformBrowser,
		Target:    api.ES2022,
		Sourcemap: api.SourceMapLinked,
		Write:     true,
		LogLevel:  api.LogLevelSilent,
	}
	if !dev {
		opts.MinifyWhitespace, opts.MinifySyntax, opts.MinifyIdentifiers = true, true, true
	}
	// SDK плагинов (/plugin-sdk/oc.js) — обычный <script>, а не модуль: IIFE и более консервативный target.
	sdk := opts
	sdk.EntryPointsAdvanced = []api.EntryPoint{{OutputPath: "oc", InputPath: "sdk/oc.ts"}}
	sdk.Format, sdk.Target = api.FormatIIFE, api.ES2019

	for _, o := range []api.BuildOptions{opts, sdk} {
		res := api.Build(o)
		for _, m := range api.FormatMessages(res.Warnings, api.FormatMessagesOptions{Kind: api.WarningMessage}) {
			fmt.Fprint(os.Stderr, m)
		}
		if len(res.Errors) > 0 {
			for _, m := range api.FormatMessages(res.Errors, api.FormatMessagesOptions{Kind: api.ErrorMessage}) {
				fmt.Fprint(os.Stderr, m)
			}
			return fmt.Errorf("esbuild: %d ошибок", len(res.Errors))
		}
	}
	fmt.Println("клиент собран →", dist)
	return nil
}

// ---- бинарники ----

type target struct{ goos, goarch, name string }

func buildBin(root, which string) error {
	all := []target{
		{"windows", "amd64", "overlay-composer-windows-amd64.exe"},
		{"linux", "amd64", "overlay-composer-linux-amd64"},
	}
	var list []target
	for _, t := range all {
		if which == "all" || which == t.goos {
			list = append(list, t)
		}
	}
	if len(list) == 0 {
		return fmt.Errorf("неизвестная цель %q (windows | linux | all)", which)
	}
	if err := buildClient(root, false); err != nil {
		return err
	}
	out := filepath.Join(root, "build")
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	version := gitVersion(root)
	for _, t := range list {
		ldflags := "-s -w -X main.version=" + version
		if t.goos == "windows" {
			ldflags += " -H=windowsgui" // GUI-подсистема: консольное окно не появляется
			if err := windowsIcon(root, t.goarch); err != nil {
				fmt.Fprintln(os.Stderr, "предупреждение: иконка exe не встроена:", err)
			}
		}
		env := []string{"CGO_ENABLED=0", "GOOS=" + t.goos, "GOARCH=" + t.goarch}
		dst := filepath.Join(out, t.name)
		if err := run(root, env, "go", "build", "-trimpath", "-ldflags", ldflags, "-o", dst, "./cmd/overlay-composer"); err != nil {
			return fmt.Errorf("%s/%s: %w", t.goos, t.goarch, err)
		}
		fmt.Println("готово →", dst)
	}
	return nil
}

// windowsIcon генерирует .syso с иконкой и манифестом; go build подхватывает его сам (имя содержит GOOS_GOARCH).
// Нужен доступ к proxy.golang.org (go run …@version); если его нет — exe соберётся без иконки.
func windowsIcon(root, arch string) error {
	syso := filepath.Join(root, "cmd", "overlay-composer", "rsrc_windows_"+arch+".syso")
	if _, err := os.Stat(syso); err == nil {
		return nil
	}
	return run(root, []string{"GOOS=", "GOARCH="}, "go", "run", "github.com/akavel/rsrc@"+rsrcVersion,
		"-arch", arch, "-ico", filepath.Join("assets", "app.ico"), "-o", syso)
}

func gitVersion(root string) string {
	cmd := exec.Command("git", "describe", "--tags", "--always", "--dirty")
	cmd.Dir = root
	if b, err := cmd.Output(); err == nil {
		if v := strings.TrimSpace(string(b)); v != "" {
			return v
		}
	}
	return "dev"
}

func clean(root string) error {
	entries, _ := os.ReadDir(filepath.Join(root, "web", "dist"))
	for _, e := range entries {
		if e.Name() == ".gitkeep" {
			continue
		}
		if err := os.RemoveAll(filepath.Join(root, "web", "dist", e.Name())); err != nil {
			return err
		}
	}
	return os.RemoveAll(filepath.Join(root, "build"))
}
