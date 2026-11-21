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

// googleKeys — единственное, что сборка берёт из .env: лишние строки файла игнорируются.
var googleKeys = []string{"GOOGLE_CLIENT_ID", "GOOGLE_CLIENT_SECRET"}

// googleCreds — ключи Google для сборки: сначала окружение (секреты CI), затем .env в корне репозитория (если он есть).
// Значения держатся в этой карте и уходят прямо в -ldflags: в окружение процесса (os.Setenv) они не попадают,
// а в логе сборки заменяются на *** (см. redact).
func googleCreds(dotEnvPath string) map[string]string {
	var file map[string]string
	if raw, err := os.ReadFile(dotEnvPath); err == nil {
		file = parseDotEnv(string(raw))
	}
	return pick(googleKeys, os.LookupEnv, file)
}

// pick берёт перечисленные ключи: из окружения (env важнее), иначе из файла.
func pick(keys []string, lookup func(string) (string, bool), file map[string]string) map[string]string {
	out := map[string]string{}
	for _, k := range keys {
		if v, ok := lookup(k); ok && v != "" {
			out[k] = v
		} else if v := file[k]; v != "" {
			out[k] = v
		}
	}
	return out
}
