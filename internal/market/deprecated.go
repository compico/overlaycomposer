package market

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"net/url"
	"regexp"
	"strings"
)

// Устаревшие плагины. Репозиторий объявляет их файлом deprecated.json в корне (рядом с plugins/):
//
//	[{ "id": "osu-np", "kind": "widget", "since": "2026-11-17", "reason": "почему и что делать",
//	   "replacedBy": { "id": "osu-tools", "kind": "wasm", "repo": "compico/oc-plugins", "url": "https://…" } }]
//
// Запись не зависит от папки плагина: она работает для wasm-плагинов (их нет в каталоге) и остаётся после удаления
// плагина из репозитория, поэтому установленная копия всё равно узнаёт, что делать.
const (
	deprecatedFile    = "deprecated.json"
	maxDeprecatedSize = 64 << 10
	maxDeprecated     = 200
	maxReason         = 500
)

// Виды плагинов в deprecated.json.
const (
	KindWidget = "widget" // plugins/<id>: ставится магазином
	KindWasm   = "wasm"   // wasm/<id>: .wasm из релиза, магазин его не ставит
)

// Replacement — чем заменить устаревший плагин.
type Replacement struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	Repo string `json:"repo"`          // owner/repo; если не указан в файле — репозиторий-источник
	URL  string `json:"url,omitempty"` // https: куда вести за плагином, который магазин не ставит (wasm — страница релизов)
}

// Deprecation — запись об устаревшем плагине.
type Deprecation struct {
	ID         string       `json:"id"`
	Kind       string       `json:"kind"`
	Since      string       `json:"since,omitempty"`
	Reason     string       `json:"reason"`
	ReplacedBy *Replacement `json:"replacedBy,omitempty"`
	Repo       string       `json:"repo"` // источник, который объявил устаревание
}

var (
	depIDRe   = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	depRepoRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	depDateRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
)

func validKind(k string) bool { return k == KindWidget || k == KindWasm }

// parseDeprecated читает deprecated.json репозитория source; битый файл и негодные записи пропускаются (плагинам от этого не хуже).
func parseDeprecated(f *zip.File, source string) []Deprecation {
	if f == nil || f.UncompressedSize64 > maxDeprecatedSize {
		return nil
	}
	raw, err := readZip(f, maxDeprecatedSize)
	if err != nil {
		return nil
	}
	var list, in []Deprecation
	if json.Unmarshal(raw, &in) != nil {
		return nil
	}
	seen := map[string]bool{}
	for _, x := range in {
		x.Reason = clip(x.Reason, maxReason)
		if !depIDRe.MatchString(x.ID) || !validKind(x.Kind) || x.Reason == "" || seen[x.Kind+"/"+x.ID] {
			continue
		}
		seen[x.Kind+"/"+x.ID] = true
		if !depDateRe.MatchString(x.Since) {
			x.Since = ""
		}
		x.Repo = source
		x.ReplacedBy = cleanReplacement(x.ReplacedBy, source)
		list = append(list, x)
		if len(list) == maxDeprecated {
			break
		}
	}
	return list
}

func cleanReplacement(r *Replacement, source string) *Replacement {
	if r == nil || !depIDRe.MatchString(r.ID) || !validKind(r.Kind) {
		return nil
	}
	out := Replacement{ID: r.ID, Kind: r.Kind, Repo: source}
	if depRepoRe.MatchString(r.Repo) {
		out.Repo = r.Repo
	}
	if u, err := url.Parse(r.URL); err == nil && u.Scheme == "https" && u.Host != "" && len(r.URL) <= 300 {
		out.URL = r.URL // ссылку открывает пользователь; http и прочее (javascript:, data:) не пропускаем
	}
	return &out
}

// InstalledDeprecation — установленный плагин, который объявлен устаревшим.
type InstalledDeprecation struct {
	Name string `json:"name"`
	Deprecation
}

// DeprecatedInstalled — какие из установленных плагинов устарели. Виджеты — установленные из магазина, по источнику
// установки; wasm-плагины (wasm: id → имя) — по id в любом из добавленных источников (магазин их не отслеживает).
// ok=false, если не ответил ни один источник.
func (m *Market) DeprecatedInstalled(ctx context.Context, refresh bool, wasm map[string]string) (list []InstalledDeprecation, ok bool) {
	list = []InstalledDeprecation{}
	installed := m.Installed()
	seen := map[string]bool{}
	for _, sv := range m.Catalog(ctx, refresh) {
		if sv.Error != "" {
			continue
		}
		ok = true
		names := map[string]string{}
		for _, e := range sv.Plugins {
			names[e.ID] = e.Name
		}
		for _, d := range sv.Deprecated {
			key := d.Kind + "/" + d.ID
			if seen[key] {
				continue
			}
			switch d.Kind {
			case KindWidget:
				rec, has := installed[d.ID]
				if !has || !strings.EqualFold(rec.Repo, sv.Repo) {
					continue
				}
				name := names[d.ID]
				if name == "" {
					name = d.ID
				}
				list = append(list, InstalledDeprecation{Name: name, Deprecation: d})
			case KindWasm:
				name, has := wasm[d.ID]
				if !has {
					continue
				}
				if name == "" {
					name = d.ID
				}
				list = append(list, InstalledDeprecation{Name: name, Deprecation: d})
			}
			seen[key] = true
		}
	}
	return list, ok
}

// parseDeprecatedZip ищет <корень>/deprecated.json в архиве репозитория.
func parseDeprecatedZip(data []byte, source string) []Deprecation {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil
	}
	for _, f := range zr.File {
		parts := strings.Split(f.Name, "/")
		if len(parts) == 2 && parts[1] == deprecatedFile && f.Mode().IsRegular() && fs.ValidPath(f.Name) {
			return parseDeprecated(f, source)
		}
	}
	return nil
}
