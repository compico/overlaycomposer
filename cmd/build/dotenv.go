package main

import (
	"bufio"
	"os"
	"strings"
)

// parseDotEnv разбирает файл .env: строки KEY=VALUE, комментарии (#) и пустые строки пропускаются, допустимы префикс export
// и значение в одинарных или двойных кавычках.
func parseDotEnv(text string) map[string]string {
	out := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		k, v, ok := strings.Cut(line, "=")
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if !ok || k == "" {
			continue
		}
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		}
		out[k] = v
	}
	return out
}

// loadDotEnv читает .env в корне репозитория (если он есть) и задаёт переменные окружения, которых ещё нет: то, что уже
// передано снаружи (например, секреты CI), важнее файла. Файл в git не попадает (.gitignore) — туда кладут локальные ключи.
func loadDotEnv(path string) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for k, v := range parseDotEnv(string(raw)) {
		if _, set := os.LookupEnv(k); !set {
			_ = os.Setenv(k, v)
		}
	}
}
